package mapr

import (
	"testing"

	"github.com/mimecast/dtail/internal/logging"
)

const mergeTestQuery = "select min(lat),max(neg),last(name),len(name),count(status),sum(bytes),avg(bytes) " +
	"from stats group by host"

// mergeTestSet describes the fields a set to merge carries. A nil pointer or
// an empty string means the field is absent from the set, as it is when no
// line of that group (in that interval, on that server) had the field.
type mergeTestSet struct {
	samples int
	lat     *float64
	neg     *float64
	name    string
	status  *float64
	bytes   *float64
}

// mergeTestWant lists the expected merged values; a nil pointer or an empty
// string means the storage key must be absent after the merge.
type mergeTestWant struct {
	samples int
	lat     *float64
	neg     *float64
	name    string
	nameLen *float64
	status  *float64
	bytes   *float64
}

func mergeFloat(f float64) *float64 { return &f }

// TestAggregateSetMergeMissingFields verifies that merging a set which lacks a
// field leaves the destination's value of that field alone: min(), max(),
// last() and len() used to be overwritten with 0 or "" (and min() of a
// positive or max() of a negative series was corrupted), and additive
// operations used to create zero-valued keys for fields no set ever had.
func TestAggregateSetMergeMissingFields(t *testing.T) {
	full := mergeTestSet{samples: 1, lat: mergeFloat(5), neg: mergeFloat(-3), name: "alice",
		status: mergeFloat(1), bytes: mergeFloat(10)}
	statusOnly := mergeTestSet{samples: 1, status: mergeFloat(1)}
	tests := []struct {
		name string
		sets []mergeTestSet
		want mergeTestWant
	}{
		{"later set lacks fields", []mergeTestSet{full, statusOnly}, mergeTestWant{samples: 2,
			lat: mergeFloat(5), neg: mergeFloat(-3), name: "alice", nameLen: mergeFloat(5),
			status: mergeFloat(2), bytes: mergeFloat(10)}},
		{"earlier set lacks fields", []mergeTestSet{statusOnly, full}, mergeTestWant{samples: 2,
			lat: mergeFloat(5), neg: mergeFloat(-3), name: "alice", nameLen: mergeFloat(5),
			status: mergeFloat(2), bytes: mergeFloat(10)}},
		{"no set has the fields", []mergeTestSet{statusOnly, statusOnly}, mergeTestWant{samples: 2,
			status: mergeFloat(2)}},
		{"every set has the fields", []mergeTestSet{full, {samples: 2, lat: mergeFloat(7),
			neg: mergeFloat(-1), name: "bob", status: mergeFloat(2), bytes: mergeFloat(4)}},
			mergeTestWant{samples: 3, lat: mergeFloat(5), neg: mergeFloat(-1), name: "bob",
				nameLen: mergeFloat(3), status: mergeFloat(3), bytes: mergeFloat(14)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := mustMergeTestQuery(t)
			dest := NewAggregateSet()
			for _, set := range tt.sets {
				if err := dest.Merge(query, buildMergeTestSet(set)); err != nil {
					t.Fatalf("Merge() error = %v", err)
				}
			}
			checkMergeTestSet(t, dest, tt.want)
		})
	}
}

// TestGlobalGroupSetMergeMissingFields drives the client's merge path: a
// group set per server message merged into the global group set, where a
// later message of the same group lacks the min/max/last/len fields.
func TestGlobalGroupSetMergeMissingFields(t *testing.T) {
	query := mustMergeTestQuery(t)
	global := NewGlobalGroupSet(logging.NopLogger{})
	for _, set := range []mergeTestSet{
		{samples: 1, lat: mergeFloat(5), neg: mergeFloat(-3), name: "alice", status: mergeFloat(1)},
		{samples: 1, status: mergeFloat(1)},
	} {
		group := NewGroupSet(logging.NopLogger{})
		*group.GetSet("host1") = *buildMergeTestSet(set)
		if err := global.Merge(query, group); err != nil {
			t.Fatalf("Merge() error = %v", err)
		}
	}
	checkMergeTestSet(t, global.GetSet("host1"), mergeTestWant{samples: 2,
		lat: mergeFloat(5), neg: mergeFloat(-3), name: "alice", nameLen: mergeFloat(5),
		status: mergeFloat(2)})
}

// TestAggregateSetMergeUnknownOperation verifies that an unknown aggregation
// still fails the merge, also when the source set lacks that field.
func TestAggregateSetMergeUnknownOperation(t *testing.T) {
	query := &Query{Select: []selectCondition{{FieldStorage: "x", Operation: AggregateOperation(-1)}}}
	for _, src := range []*AggregateSet{NewAggregateSet(), {FValues: map[string]float64{"x": 1}}} {
		if err := NewAggregateSet().Merge(query, src); err == nil {
			t.Fatalf("Merge(%v) error = nil, want unknown aggregation error", src)
		}
	}
}

func mustMergeTestQuery(t *testing.T) *Query {
	t.Helper()
	query, err := NewQuery(mergeTestQuery, logging.NopLogger{})
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}
	return query
}

// buildMergeTestSet builds a set the way the aggregation does: a storage key
// exists only when a line of the group had the field.
func buildMergeTestSet(in mergeTestSet) *AggregateSet {
	set := NewAggregateSet()
	set.Samples = in.samples
	putFloat := func(key string, value *float64) {
		if value != nil {
			set.FValues[key] = *value
		}
	}
	putFloat("min(lat)", in.lat)
	putFloat("max(neg)", in.neg)
	putFloat("count(status)", in.status)
	putFloat("sum(bytes)", in.bytes)
	putFloat("avg(bytes)", in.bytes)
	if in.name != "" {
		set.SValues["last(name)"] = in.name
		set.SValues["len(name)"] = in.name
		set.FValues["len(name)"] = float64(len(in.name))
	}
	return set
}

func checkMergeTestSet(t *testing.T, set *AggregateSet, want mergeTestWant) {
	t.Helper()
	if set.Samples != want.samples {
		t.Errorf("Samples = %d, want %d", set.Samples, want.samples)
	}
	checkFloat := func(key string, want *float64) {
		got, ok := set.FValues[key]
		switch {
		case want == nil && ok:
			t.Errorf("%s = %v, want the key absent", key, got)
		case want != nil && !ok:
			t.Errorf("%s absent, want %v", key, *want)
		case want != nil && got != *want:
			t.Errorf("%s = %v, want %v", key, got, *want)
		}
	}
	checkFloat("min(lat)", want.lat)
	checkFloat("max(neg)", want.neg)
	checkFloat("len(name)", want.nameLen)
	checkFloat("count(status)", want.status)
	checkFloat("sum(bytes)", want.bytes)
	checkFloat("avg(bytes)", want.bytes)
	for _, key := range []string{"last(name)", "len(name)"} {
		got, ok := set.SValues[key]
		if ok != (want.name != "") || got != want.name {
			t.Errorf("%s = %q (present %v), want %q", key, got, ok, want.name)
		}
	}
}
