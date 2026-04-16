package logformat

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/mapr"
)

// ErrIgnoreFields indicates that the fields should be ignored.
var ErrIgnoreFields error = errors.New("Ignore this field set")

// Parser is used to parse the mapreduce information from the server log files.
type Parser interface {
	// MakeFields creates a field map from an input log line. The sourceID
	// identifies the log file (or stream) the line belongs to so that
	// stateful parsers (e.g. CSV with per-file headers) can key their
	// state per source instead of smearing it across every file in a
	// session.
	MakeFields(maprLine, sourceID string) (map[string]string, error)
}

type queryAwareParser interface {
	setQuery(*mapr.Query)
}

// ParserFactory builds a Parser for a specific log format.
type ParserFactory func(hostname, timeZoneName string, timeZoneOffset int) (Parser, error)

var parserFactories = make(map[string]ParserFactory)
var parserFactoriesMu sync.RWMutex

func init() {
	registerBuiltInParsers()
}

// RegisterParser registers or replaces a parser factory for a log format name.
func RegisterParser(logFormatName string, factory ParserFactory) error {
	name := strings.TrimSpace(logFormatName)
	if name == "" {
		return errors.New("log format name cannot be empty")
	}
	if factory == nil {
		return errors.New("parser factory cannot be nil")
	}

	parserFactoriesMu.Lock()
	defer parserFactoriesMu.Unlock()
	parserFactories[name] = factory
	return nil
}

func getParserFactory(logFormatName string) (ParserFactory, bool) {
	parserFactoriesMu.RLock()
	defer parserFactoriesMu.RUnlock()
	factory, found := parserFactories[logFormatName]
	return factory, found
}

func registerBuiltInParsers() {
	mustRegisterParser("generic", wrapParserFactory(newGenericParser))
	mustRegisterParser("generickv", wrapParserFactory(newGenericKVParser))
	mustRegisterParser("csv", wrapParserFactory(newCSVParser))
	mustRegisterParser("mimecast", wrapParserFactory(newMimecastParser))
	mustRegisterParser("mimecastgeneric", wrapParserFactory(newMimecastGenericParser))
	mustRegisterParser("default", wrapParserFactory(newDefaultParser))
	mustRegisterParser("custom1", wrapParserFactory(newCustom1Parser))
	mustRegisterParser("custom2", wrapParserFactory(newCustom2Parser))
}

func mustRegisterParser(logFormatName string, factory ParserFactory) {
	if err := RegisterParser(logFormatName, factory); err != nil {
		panic(err)
	}
}

func wrapParserFactory[T Parser](factory func(string, string, int) (T, error)) ParserFactory {
	return func(hostname, timeZoneName string, timeZoneOffset int) (Parser, error) {
		return factory(hostname, timeZoneName, timeZoneOffset)
	}
}

// NewParser returns a new log parser.
func NewParser(logFormatName string, query *mapr.Query) (Parser, error) {
	hostname, err := config.Hostname()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	timeZoneName, timeZoneOffset := now.Zone()

	if parserFactory, found := getParserFactory(logFormatName); found {
		parser, err := parserFactory(hostname, timeZoneName, timeZoneOffset)
		configureParserQuery(parser, query)
		return parser, err
	}

	defaultFactory, found := getParserFactory("default")
	if !found {
		return nil, fmt.Errorf("No '%s' mapr log format and no default parser registered", logFormatName)
	}

	p, err := defaultFactory(hostname, timeZoneName, timeZoneOffset)
	if err != nil {
		return p, fmt.Errorf("No '%s' mapr log format and problem creating default one: %v",
			logFormatName, err)
	}
	configureParserQuery(p, query)
	return p, fmt.Errorf("No '%s' mapr log format", logFormatName)
}

func configureParserQuery(parser Parser, query *mapr.Query) {
	if parser == nil {
		return
	}
	queryAware, ok := parser.(queryAwareParser)
	if !ok {
		return
	}
	queryAware.setQuery(query)
}
