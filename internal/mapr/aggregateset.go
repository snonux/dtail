package mapr

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/protocol"
)

// AggregateSet represents aggregated key/value pairs from the
// MAPREDUCE log lines. These could be either string values or float
// values.
type AggregateSet struct {
	Samples int
	FValues map[string]float64
	SValues map[string]string
}

// NewAggregateSet creates a new empty aggregate set.
func NewAggregateSet() *AggregateSet {
	return &AggregateSet{
		FValues: make(map[string]float64),
		SValues: make(map[string]string),
	}
}

// String representation of aggregate set.
func (s *AggregateSet) String() string {
	return fmt.Sprintf("AggregateSet(Samples:%d,FValues:%v,SValues:%v)",
		s.Samples, s.FValues, s.SValues)
}

// Merge one aggregate set into this one. set is treated as the newer of the
// two: where both carry a last() or len() value, set's value wins.
//
// A field is merged only when set carries its storage key. A set has a key
// only when a line of its group had the field (sparse fields, where clauses
// not requiring it, a later interval, or another server's lines), so an
// absent key means "no value", not 0 or "": merging it as one would turn
// min() of positive or max() of negative values into 0 and blank last() and
// len(). Skipping it also keeps additive operations from creating keys no
// line ever had: they render as 0 either way, but percentile() ranks only the
// groups that have the key (GroupSet.makeResultStats).
//
// On error (an unknown aggregation, which the query parser rejects) s is left
// unchanged: every operation is checked before anything is merged, so a caller
// never ends up with a partially merged set. Both passes look the operation up
// in fieldMergerFor, the single place that knows how each operation merges.
func (s *AggregateSet) Merge(query *Query, set *AggregateSet) error {
	for _, sc := range query.Select {
		if fieldMergerFor(sc.Operation) == nil {
			return fmt.Errorf("unknown aggregation method '%v'", sc.Operation)
		}
	}
	s.Samples += set.Samples
	for _, sc := range query.Select {
		fieldMergerFor(sc.Operation)(s, sc.FieldStorage, set)
	}
	return nil
}

// Serialize the aggregate set so it can be sent over the wire. Returns true
// when the serialized message was successfully sent, and false when the
// context was cancelled before the send completed. Callers that own the
// source state (e.g. Aggregate) must re-merge unsent sets so data is
// not silently lost.
func (s *AggregateSet) Serialize(ctx context.Context, groupKey string, ch chan<- string,
	logger logging.Logger) bool {

	logger = logging.OrNop(logger)
	if logger.TraceEnabled() {
		logger.Trace("Serialising mapr.AggregateSet", s)
	}
	sb := pool.BuilderBuffer.Get().(*strings.Builder)
	defer pool.RecycleBuilderBuffer(sb)

	sb.WriteString(groupKey)
	sb.WriteString(protocol.AggregateDelimiter)
	sb.WriteString(strconv.Itoa(s.Samples))
	sb.WriteString(protocol.AggregateDelimiter)

	for k, v := range s.FValues {
		sb.WriteString(k)
		sb.WriteString(protocol.AggregateKVDelimiter)
		sb.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
		sb.WriteString(protocol.AggregateDelimiter)
	}

	for k, v := range s.SValues {
		sb.WriteString(k)
		sb.WriteString(protocol.AggregateKVDelimiter)
		sb.WriteString(v)
		sb.WriteString(protocol.AggregateDelimiter)
	}

	select {
	case ch <- sb.String():
		return true
	case <-ctx.Done():
		return false
	}
}

// fieldMerger merges the value of one storage key of set into s, if set has
// one (see Merge).
type fieldMerger func(s *AggregateSet, storage string, set *AggregateSet)

// fieldMergerFor returns how op merges, or nil when Merge does not know op.
// It is the only list of mergeable operations: Merge uses it both to reject an
// unknown operation and to merge, so an operation cannot be accepted by the
// check but then silently skipped by the merge. A new operation must be added
// here (TestFieldMergerForCoversAggregate checks it against Aggregate).
func fieldMergerFor(op AggregateOperation) fieldMerger {
	switch op {
	case Count, Sum, Avg, Percentage, Percentile:
		return mergeAdditive
	case Min:
		return mergeMin
	case Max:
		return mergeMax
	case Last:
		return mergeLast
	case Len:
		return mergeLen
	default:
		return nil
	}
}

func mergeAdditive(s *AggregateSet, storage string, set *AggregateSet) {
	if value, ok := set.FValues[storage]; ok {
		s.addFloat(storage, value)
	}
}

func mergeMin(s *AggregateSet, storage string, set *AggregateSet) {
	if value, ok := set.FValues[storage]; ok {
		s.addFloatMin(storage, value)
	}
}

func mergeMax(s *AggregateSet, storage string, set *AggregateSet) {
	if value, ok := set.FValues[storage]; ok {
		s.addFloatMax(storage, value)
	}
}

func mergeLast(s *AggregateSet, storage string, set *AggregateSet) {
	if value, ok := set.SValues[storage]; ok {
		s.setString(storage, value)
	}
}

// mergeLen merges a len() value. Aggregate stores it as the string and its
// length under the same key, so the string's presence stands for both.
func mergeLen(s *AggregateSet, storage string, set *AggregateSet) {
	if value, ok := set.SValues[storage]; ok {
		s.setString(storage, value)
		s.setFloat(storage, set.FValues[storage])
	}
}

// Add a float value.
func (s *AggregateSet) addFloat(key string, value float64) {
	if _, ok := s.FValues[key]; !ok {
		s.FValues[key] = value
		return
	}
	s.FValues[key] += value
}

// Add a float minimum value.
func (s *AggregateSet) addFloatMin(key string, value float64) {
	f, ok := s.FValues[key]
	if !ok {
		s.FValues[key] = value
		return
	}
	if f > value {
		s.FValues[key] = value
	}
}

// Add a float maximum value.
func (s *AggregateSet) addFloatMax(key string, value float64) {
	f, ok := s.FValues[key]
	if !ok {
		s.FValues[key] = value
		return
	}
	if f < value {
		s.FValues[key] = value
	}
}

// Set a string.
func (s *AggregateSet) setString(key, value string) {
	s.SValues[key] = value
}

// Set a float.
func (s *AggregateSet) setFloat(key string, value float64) {
	s.FValues[key] = value
}

// Aggregate data to the aggregate set. The value is only borrowed for the
// duration of the call: every string the set keeps is stored as a copy.
func (s *AggregateSet) Aggregate(key string, agg AggregateOperation, value string, clientAggregation bool) (err error) {
	var f float64
	// First check if we can aggregate anything without converting value to float.
	switch agg {
	case Count:
		if clientAggregation {
			f, err = strconv.ParseFloat(value, 64)
			if err != nil {
				return
			}
			s.addFloat(key, f)
			return
		}
		s.addFloat(key, 1)
		return
	case Last:
		// value may borrow memory that the caller reuses for the next line
		// (see aggregate.parseLine), so anything retained must be a copy.
		s.setString(key, strings.Clone(value))
		return
	case Len:
		s.setString(key, strings.Clone(value))
		s.setFloat(key, float64(len(value)))
		return
	default:
	}

	// No, we have to convert to float.
	f, err = strconv.ParseFloat(value, 64)
	if err != nil {
		return
	}

	switch agg {
	case Sum:
		fallthrough
	case Avg:
		fallthrough
	case Percentage:
		fallthrough
	case Percentile:
		s.addFloat(key, f)
	case Min:
		s.addFloatMin(key, f)
	case Max:
		s.addFloatMax(key, f)
	default:
		err = fmt.Errorf("unknown aggregation method '%v'", agg)
	}
	return
}
