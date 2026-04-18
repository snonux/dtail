package server

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
)

// Aggregate is for aggregating mapreduce data on the DTail server side.
type Aggregate struct {
	done *internal.Done
	// NextLinesCh can be used to use a new line ch.
	NextLinesCh chan chan *line.Line
	linesCh     chan *line.Line
	// Hostname of the current server (used to populate $hostname field).
	hostname string
	// Signals to serialize data.
	serialize chan struct{}
	// The mapr query
	query *mapr.Query
	// The mapr log format parser
	parser logformat.Parser
	// mu protects concurrent access to channel switching
	mu sync.Mutex
}

// NewAggregate return a new server side aggregator.
func NewAggregate(queryStr string, defaultLogFormat string) (*Aggregate, error) {
	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		return nil, err
	}

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.Error(err)
	}
	s := strings.Split(fqdn, ".")

	parserName := resolveParserName(query, defaultLogFormat)

	dlog.Server.Info("Creating log format parser", parserName)
	logParser, err := logformat.NewParser(parserName, query)
	if err != nil {
		dlog.Server.Error("Could not create log format parser. Falling back to 'generic'", err)
		if logParser, err = logformat.NewParser("generic", query); err != nil {
			dlog.Server.FatalPanic("Could not create log format parser", err)
		}
	}

	return &Aggregate{
		done:        internal.NewDone(),
		NextLinesCh: make(chan chan *line.Line, 10000), // Increased buffer for high concurrency
		serialize:   make(chan struct{}),
		hostname:    s[0],
		query:       query,
		parser:      logParser,
	}, nil
}

// Shutdown the aggregation engine.
func (a *Aggregate) Shutdown() {
	a.done.Shutdown()
}

// Start an aggregation.
func (a *Aggregate) Start(ctx context.Context, maprMessages chan<- string) {
	myCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-myCtx.Done():
			a.done.Shutdown()
		case <-a.done.Done():
			cancel()
		}
	}()

	fieldsCh := a.fieldsFromLines(myCtx)
	// Add fields (e.g. via 'set' clause)
	if len(a.query.Set) > 0 {
		fieldsCh = a.setAdditionalFields(myCtx, fieldsCh)
	}
	// Periodically pre-aggregate data every a.query.Interval seconds.
	go a.aggregateTimer(myCtx)
	a.aggregateAndSerialize(myCtx, fieldsCh, maprMessages)
}

func (a *Aggregate) aggregateTimer(ctx context.Context) {
	interval := a.query.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.Serialize(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (a *Aggregate) nextLine() (l *line.Line, ok bool, noMoreChannels bool) {
	dlog.Server.Trace("nextLine.enter", l, ok, noMoreChannels)

	// Protect channel operations with mutex to prevent races while switching
	// between per-file line channels.
	a.mu.Lock()
	defer a.mu.Unlock()

	select {
	case l, ok = <-a.linesCh:
		if !ok {
			// Channel is closed, go to next channel. The next reader goroutine
			// may still be registering its channel, so wait briefly before
			// concluding that the aggregate is finished.
			select {
			case a.linesCh = <-a.NextLinesCh:
			case <-time.After(100 * time.Millisecond):
				select {
				case a.linesCh = <-a.NextLinesCh:
				default:
					noMoreChannels = true
				}
			}
		}
	default:
		// Keep reading the current file until its channel is closed. Switching
		// away from a merely idle channel can drop unread lines from that file.
	}
	dlog.Server.Trace("nextLine.exit", l, ok, noMoreChannels)
	return
}

func (a *Aggregate) fieldsFromLines(ctx context.Context) <-chan map[string]string {
	fieldsCh := make(chan map[string]string)

	go func() {
		defer close(fieldsCh)

		// Gather first lines channel (first input file)
		a.mu.Lock()
		select {
		case a.linesCh = <-a.NextLinesCh:
		case <-ctx.Done():
			a.mu.Unlock()
			return
		}
		a.mu.Unlock()

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// Gather first lines channel (first input file)
			line, ok, noMoreChannels := a.nextLine()
			if !ok {
				if noMoreChannels {
					return
				}
				time.Sleep(time.Millisecond * 100)
				continue
			}

			if err := a.fieldFromLine(ctx, line, fieldsCh); err != nil {
				dlog.Server.Error(err)
			}
		}
	}()

	return fieldsCh
}

func (a *Aggregate) fieldFromLine(ctx context.Context, line *line.Line,
	fieldsCh chan<- map[string]string) error {

	maprLine := strings.TrimSpace(line.Content.String())
	sourceID := line.SourceID

	// after recycling it, don't use line object anymore!!!
	line.Recycle()
	fields, err := a.parser.MakeFields(maprLine, sourceID)

	if err != nil {
		// Should fields be ignored anyway?
		if err != logformat.ErrIgnoreFields {
			return err
		}
		return nil
	}
	if !a.query.WhereClause(fields) {
		return nil
	}

	select {
	case fieldsCh <- fields:
	case <-ctx.Done():
	}

	return nil
}

func (a *Aggregate) setAdditionalFields(ctx context.Context,
	fieldsCh <-chan map[string]string) <-chan map[string]string {

	newFieldsCh := make(chan map[string]string)
	go func() {
		defer close(newFieldsCh)
		for {
			fields, ok := <-fieldsCh
			if !ok {
				return
			}
			if err := a.query.SetClause(fields); err != nil {
				dlog.Server.Error(err)
			}

			select {
			case newFieldsCh <- fields:
			case <-ctx.Done():
			}
		}
	}()
	return newFieldsCh
}

func (a *Aggregate) aggregateAndSerialize(ctx context.Context,
	fieldsCh <-chan map[string]string, maprMessages chan<- string) {

	group := mapr.NewGroupSet()
	serialize := func() {
		dlog.Server.Info("Serializing mapreduce result")
		remaining := group.Serialize(ctx, maprMessages)
		// Preserve unsent entries so the next tick (or the caller driving the
		// loop) can retry them. Dropping them here would silently lose data
		// whenever the serialize context fires mid-send.
		if len(remaining) > 0 {
			dlog.Server.Warn("Aggregate serialize interrupted; preserving unsent groups",
				"remaining", len(remaining))
		}
		group.ResetWith(remaining)
	}
	for {
		select {
		case fields, ok := <-fieldsCh:
			if !ok {
				serialize()
				return
			}
			a.aggregate(group, fields)
		case <-a.serialize:
			serialize()
		case <-ctx.Done():
			return
		}
	}
}

func (a *Aggregate) aggregate(group *mapr.GroupSet, fields map[string]string) {
	groupKey := buildGroupKey(a.query.GroupBy, fields)
	set := group.GetSet(groupKey)

	var addedSample bool
	for _, sc := range a.query.Select {
		if val, ok := fields[sc.Field]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
				dlog.Server.Error(err)
				continue
			}
			addedSample = true
		}
	}

	if addedSample {
		set.Samples++
		return
	}
	dlog.Server.Trace("Aggregated data locally without adding new samples")
}

// Serialize all the aggregated data.
func (a *Aggregate) Serialize(ctx context.Context) {
	select {
	case a.serialize <- struct{}{}:
	case <-time.After(time.Minute):
		dlog.Server.Warn("Starting to serialize mapredice data takes over a minute")
	case <-ctx.Done():
	}
}
