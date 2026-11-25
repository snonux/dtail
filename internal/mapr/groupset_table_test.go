package mapr

import (
	"reflect"
	"testing"

	"github.com/mimecast/dtail/internal/logging"
)

// TestGroupSetResultTable checks the result as data: the select fields as
// columns and the rows in the order of the printed table, without the
// query's limit applied.
func TestGroupSetResultTable(t *testing.T) {
	query, err := NewQuery("select count(status),host from stats group by host order by count(status) limit 1",
		logging.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	global := NewGlobalGroupSet(logging.NopLogger{})
	for host, count := range map[string]int{"host-a": 1, "host-b": 3} {
		set := global.GetSet(host)
		for range count {
			if err = set.Aggregate("count(status)", Count, "200", false); err != nil {
				t.Fatal(err)
			}
		}
		if err = set.Aggregate("host", Last, host, false); err != nil {
			t.Fatal(err)
		}
	}
	table, err := global.ResultTable(query)
	if err != nil {
		t.Fatal(err)
	}
	want := ResultTable{
		Columns: []string{"count(status)", "host"},
		Rows:    [][]string{{"3", "host-b"}, {"1", "host-a"}},
	}
	if !reflect.DeepEqual(table, want) {
		t.Fatalf("ResultTable() = %+v, want %+v", table, want)
	}
}
