package client

import (
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/protocol"
)

func TestAggregateResetsPendingLocalStateOnGenerationChange(t *testing.T) {
	query := mustSessionStateQuery(t, "select status,count(status) from stats group by status")
	state := NewSessionState(query)
	aggregate := NewAggregate("srv1", state)
	countStorage := aggregateCountStorage(t, query)

	oldSet := aggregate.group.GetSet("ERROR")
	oldSet.Samples = 1
	oldSet.FValues[countStorage] = 1

	rawQuery := "select status,count(status) from warnings group by status"
	if _, err := state.CommitQuery(rawQuery, 2); err != nil {
		t.Fatalf("CommitQuery() error = %v", err)
	}

	snapshot := state.Snapshot()
	message := strings.Join([]string{
		"WARN",
		"1",
		aggregateCountStorage(t, snapshot.Query) + protocol.AggregateKVDelimiter + "1",
		"",
	}, protocol.AggregateDelimiter)

	if err := aggregate.Aggregate(message); err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}

	result, numRows, err := snapshot.GlobalGroup.Result(snapshot.Query, 10, nil)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if numRows != 1 {
		t.Fatalf("numRows = %d, want 1", numRows)
	}
	if !strings.Contains(result, "1") {
		t.Fatalf("expected one new-generation aggregate row, got %q", result)
	}
}

func TestAggregateRejectsMalformedMessage(t *testing.T) {
	query := mustSessionStateQuery(t, "select count(status) from stats group by status")
	state := NewSessionState(query)
	aggregate := NewAggregate("srv1", state)

	if err := aggregate.Aggregate("broken"); err == nil {
		t.Fatalf("expected Aggregate() to reject malformed messages")
	}
}

func aggregateCountStorage(t *testing.T, query *mapr.Query) string {
	t.Helper()

	for _, selectCondition := range query.Select {
		if selectCondition.Operation == mapr.Count {
			return selectCondition.FieldStorage
		}
	}
	t.Fatalf("query %q does not contain count() storage", query.RawQuery)
	return ""
}
