package client

import (
	"testing"

	"github.com/mimecast/dtail/internal/mapr"
)

func TestSessionStateCommitQueryResetsGenerationAndResults(t *testing.T) {
	query := mustSessionStateQuery(t, "select count(status) from stats group by status")
	state := NewSessionState(query)

	initial := state.Snapshot()
	group := mapr.NewGroupSet()
	set := group.GetSet("ERROR")
	set.Samples = 1
	set.FValues[query.Select[0].FieldStorage] = 1
	if err := initial.GlobalGroup.Merge(query, group); err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if changed, ok := state.CommitRenderedResult(initial.Generation, "old-result"); !ok || !changed {
		t.Fatalf("CommitRenderedResult() = changed:%v ok:%v, want changed and ok", changed, ok)
	}

	rawQuery := "select count(status) from warnings group by status"
	updatedQuery, err := state.CommitQuery(rawQuery, 3)
	if err != nil {
		t.Fatalf("CommitQuery() error = %v", err)
	}
	if updatedQuery == nil || updatedQuery.RawQuery != rawQuery {
		t.Fatalf("unexpected updated query: %#v", updatedQuery)
	}

	select {
	case <-state.Changes():
	default:
		t.Fatalf("expected change notification after CommitQuery")
	}

	updated := state.Snapshot()
	if updated.Generation != 3 {
		t.Fatalf("generation = %d, want 3", updated.Generation)
	}
	if updated.Query == nil || updated.Query.RawQuery != rawQuery {
		t.Fatalf("unexpected query after commit: %#v", updated.Query)
	}
	if !updated.GlobalGroup.IsEmpty() {
		t.Fatalf("expected committed global group to be reset")
	}
	if updated.LastResult != "" {
		t.Fatalf("last result = %q, want empty", updated.LastResult)
	}
}

func TestSessionStateCommitQueryRejectsInvalidQuery(t *testing.T) {
	query := mustSessionStateQuery(t, "select count(status) from stats group by status")
	state := NewSessionState(query)
	before := state.Snapshot()

	if _, err := state.CommitQuery("select from", 5); err == nil {
		t.Fatalf("expected CommitQuery() to reject invalid query")
	}

	after := state.Snapshot()
	if after.Generation != before.Generation {
		t.Fatalf("generation changed on invalid query: got %d want %d", after.Generation, before.Generation)
	}
	if after.Query == nil || after.Query.RawQuery != before.Query.RawQuery {
		t.Fatalf("query changed on invalid query: before=%#v after=%#v", before.Query, after.Query)
	}
}

func mustSessionStateQuery(t *testing.T, queryStr string) *mapr.Query {
	t.Helper()

	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		t.Fatalf("NewQuery(%q) error = %v", queryStr, err)
	}
	return query
}
