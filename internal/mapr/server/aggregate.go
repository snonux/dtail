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
	"github.com/mimecast/dtail/internal/protocol"
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
func NewAggregate(queryStr string) (*Aggregate, error) {
	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		return nil, err
	}

	fqdn, err := config.Hostname()
	if err != nil {
		dlog.Server.Error(err)
	}
	s := strings.Split(fqdn, ".")

	var parserName string
	switch query.LogFormat {
	case "":
		parserName = config.Server.MapreduceLogFormat
		if query.Table == "" {
			parserName = "generic"
		}
	default:
		parserName = query.LogFormat
	}

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
	for {
		select {
		case <-time.After(a.query.Interval):
			a.Serialize(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (a *Aggregate) nextLine() (l *line.Line, ok bool, noMoreChannels bool) {
	dlog.Server.Trace("nextLine.enter", l, ok, noMoreChannels)

	// Protect channel operations with mutex to prevent race conditions
	a.mu.Lock()
	defer a.mu.Unlock()

	select {
	case l, ok = <-a.linesCh:
		if !ok {
			// Channel is closed, go to next channel.
			select {
			case a.linesCh = <-a.NextLinesCh:
			default:
				noMoreChannels = true
			}
		}
	default:
		// No new line from current lines channel. Try next one.
		select {
		case newLinesCh := <-a.NextLinesCh:
			oldLinesCh := a.linesCh
			a.linesCh = newLinesCh
			
			// Ensure the old channel is fully drained before recycling to prevent data mixing
			go func(oldCh chan *line.Line) {
				// First, drain any remaining lines from the old channel
				drained := 0
				drainLoop:
				for {
					select {
					case l, ok := <-oldCh:
						if !ok {
							// Channel is closed, safe to recycle
							break drainLoop
						}
						if l != nil {
							l.Recycle()
							drained++
						}
					default:
						// No more lines to drain immediately
						break drainLoop
					}
				}
				
				if drained > 0 {
					dlog.Server.Debug("Drained", drained, "lines from recycled channel")
				}
				
				// Now safely recycle the drained channel
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case a.NextLinesCh <- oldCh:
				case <-timer.C:
					dlog.Server.Warn("Timeout: failed to put channel back, NextLinesCh might be full")
				}
			}(oldLinesCh)
		default:
			// No new lines channel found.
		}
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

	// after recycling it, don't use line object anymore!!!
	line.Recycle()
	fields, err := a.parser.MakeFields(maprLine)

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
		group.Serialize(ctx, maprMessages)
		group = mapr.NewGroupSet()
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
	var sb strings.Builder
	for i, field := range a.query.GroupBy {
		if i > 0 {
			sb.WriteString(protocol.AggregateGroupKeyCombinator)
		}
		if val, ok := fields[field]; ok {
			sb.WriteString(val)
		}
	}
	groupKey := sb.String()
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


