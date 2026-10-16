package aggregate

import (
	"testing"

	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
)

// TestSerializerMergeRemainingMissingFields verifies that re-merging an unsent
// group with a newer live set of it merges only the fields a set carries: a
// value the other set lacks survives, a field neither set has gets no key
// (formerly min()/max() got a 0 that the next serialization sent), and the
// newer live set's last() value wins where both have one.
func TestSerializerMergeRemainingMissingFields(t *testing.T) {
	query, err := mapr.NewQuery(
		"select min(lat),max(neg),min(absent),last(name),count(status) from stats group by host",
		logging.NopLogger{})
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}
	s := newSerializer(query, logging.NopLogger{})
	s.groupSets["both"] = &mapr.AggregateSet{Samples: 1,
		FValues: map[string]float64{"count(status)": 1},
		SValues: map[string]string{"last(name)": "new"}}
	s.groupSets["liveonly"] = &mapr.AggregateSet{Samples: 2,
		FValues: map[string]float64{"count(status)": 2}, SValues: map[string]string{}}

	s.mergeRemaining(map[string]*mapr.AggregateSet{
		"both": {Samples: 2, FValues: map[string]float64{"min(lat)": 5, "max(neg)": -3,
			"count(status)": 2}, SValues: map[string]string{"last(name)": "old"}},
		"unsentonly": {Samples: 1, FValues: map[string]float64{"min(lat)": 7},
			SValues: map[string]string{}},
	})

	both := s.groupSets["both"]
	if both.Samples != 3 || both.FValues["count(status)"] != 3 {
		t.Errorf("both: Samples=%d count=%v, want 3 and 3", both.Samples, both.FValues["count(status)"])
	}
	if both.FValues["min(lat)"] != 5 || both.FValues["max(neg)"] != -3 {
		t.Errorf("both: min(lat)=%v max(neg)=%v, want 5 and -3",
			both.FValues["min(lat)"], both.FValues["max(neg)"])
	}
	if got := both.SValues["last(name)"]; got != "new" {
		t.Errorf("both: last(name)=%q, want the newer live value %q", got, "new")
	}
	for key, set := range s.groupSets {
		if value, ok := set.FValues["min(absent)"]; ok {
			t.Errorf("%s: min(absent)=%v, want the key absent", key, value)
		}
	}
	if got := s.groupSets["liveonly"].FValues["count(status)"]; got != 2 {
		t.Errorf("liveonly: count=%v, want 2", got)
	}
	if got := s.groupSets["unsentonly"].FValues["min(lat)"]; got != 7 {
		t.Errorf("unsentonly: min(lat)=%v, want 7", got)
	}
}
