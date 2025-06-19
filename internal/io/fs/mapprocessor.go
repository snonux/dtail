package fs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/logformat"
	"github.com/mimecast/dtail/internal/protocol"
)

// MapProcessor handles MapReduce-style aggregation
type MapProcessor struct {
	plain          bool
	hostname       string
	query          *mapr.Query
	parser         logformat.Parser
	groupSet       *mapr.GroupSet
	buffer         []byte
	output         io.Writer
	lastSerialized time.Time
	serializeFunc  func(groupSet *mapr.GroupSet)
}

// NewMapProcessor creates a new map processor
func NewMapProcessor(plain bool, hostname string, queryStr string, output io.Writer) (*MapProcessor, error) {
	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		return nil, err
	}

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

	logParser, err := logformat.NewParser(parserName, query)
	if err != nil {
		dlog.Server.Error("Could not create log format parser. Falling back to 'generic'", err)
		if logParser, err = logformat.NewParser("generic", query); err != nil {
			return nil, fmt.Errorf("could not create log format parser: %w", err)
		}
	}

	mp := &MapProcessor{
		plain:          plain,
		hostname:       hostname,
		query:          query,
		parser:         logParser,
		groupSet:       mapr.NewGroupSet(),
		buffer:         make([]byte, 0, 1024*1024), // 1MB buffer for aggregation
		output:         output,
		lastSerialized: time.Now(),
	}

	// Set up serialization function
	mp.serializeFunc = mp.defaultSerializeFunc

	return mp, nil
}

// SetSerializeFunc allows custom serialization (for testing or different output formats)
func (mp *MapProcessor) SetSerializeFunc(fn func(groupSet *mapr.GroupSet)) {
	mp.serializeFunc = fn
}

func (mp *MapProcessor) Initialize(ctx context.Context) error {
	return nil
}

func (mp *MapProcessor) Cleanup() error {
	return nil
}

// ProcessLine processes a single line for MapReduce aggregation.
// Parses the line, applies WHERE and SET clauses, aggregates matching fields,
// and handles periodic serialization. Returns nil (no immediate output for MapReduce).
func (mp *MapProcessor) ProcessLine(line []byte, lineNum int, filePath string, stats *stats, sourceID string) ([]byte, bool) {
	// Convert line to string and parse fields
	maprLine := strings.TrimSpace(string(line))

	fields, err := mp.parser.MakeFields(maprLine)
	if err != nil {
		// Should fields be ignored anyway?
		if err != logformat.ErrIgnoreFields {
			dlog.Server.Error("Error parsing line for MapReduce", err)
		}
		return nil, false
	}

	// Apply WHERE clause filter
	if !mp.query.WhereClause(fields) {
		return nil, false
	}

	// Apply SET clause (add additional fields)
	if len(mp.query.Set) > 0 {
		if err := mp.query.SetClause(fields); err != nil {
			dlog.Server.Error("Error applying SET clause", err)
			return nil, false
		}
	}

	// Aggregate the fields
	mp.aggregateFields(fields)

	// Check if we should serialize results periodically (every 5 seconds by default)
	now := time.Now()
	if now.Sub(mp.lastSerialized) >= mp.query.Interval {
		mp.periodicSerialize()
		mp.lastSerialized = now
	}

	return nil, false // No immediate output for MapReduce - output happens periodically
}

// aggregateFields groups parsed fields by the GROUP BY clause and aggregates values
// according to the SELECT operations. Creates a group key from GROUP BY fields
// and updates the corresponding aggregation set with SELECT field values.
func (mp *MapProcessor) aggregateFields(fields map[string]string) {
	var sb strings.Builder
	for i, field := range mp.query.GroupBy {
		if i > 0 {
			sb.WriteString(protocol.AggregateGroupKeyCombinator)
		}
		if val, ok := fields[field]; ok {
			sb.WriteString(val)
		}
	}
	groupKey := sb.String()
	set := mp.groupSet.GetSet(groupKey)

	var addedSample bool
	for _, sc := range mp.query.Select {
		if val, ok := fields[sc.Field]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, false); err != nil {
				dlog.Server.Error("Error aggregating field", err)
				continue
			}
			addedSample = true
		}
	}

	if addedSample {
		set.Samples++
	}
}

// periodicSerialize sends current aggregation results and resets the group set
func (mp *MapProcessor) periodicSerialize() {
	if mp.serializeFunc != nil {
		mp.serializeFunc(mp.groupSet)
	}
	// Reset group set for next interval
	mp.groupSet = mapr.NewGroupSet()
}

// defaultSerializeFunc implements the default serialization behavior for MapReduce results.
// This function is called periodically to send aggregated data to the client.
// It uses a channel-based approach to serialize the group set and format output
// according to the DTail protocol (A|serialized_data¬) for transmission.
func (mp *MapProcessor) defaultSerializeFunc(groupSet *mapr.GroupSet) {
	// Use a channel to collect serialized data
	ch := make(chan string, 100)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for msg := range ch {
			// Format as protocol message: A|{serialized_data}¬
			var output strings.Builder
			output.WriteString("A")
			output.WriteString(protocol.FieldDelimiter)
			output.WriteString(msg)
			output.WriteByte(protocol.MessageDelimiter)

			// Write to output immediately
			if mp.output != nil {
				mp.output.Write([]byte(output.String()))
			}
		}
	}()

	// Serialize the group set
	ctx := context.Background()
	groupSet.Serialize(ctx, ch)
	close(ch)
	<-done
}

func (mp *MapProcessor) Flush() []byte {
	// Final flush - serialize any remaining data
	if mp.serializeFunc != nil {
		mp.serializeFunc(mp.groupSet)
	}
	return nil // Output is handled by serializeFunc
}
