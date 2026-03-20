package handlers

import (
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/protocol"
)

func TestMaprHandlerShutdownFlushesPendingAggregateState(t *testing.T) {
	query, err := mapr.NewQuery("select status,count(status) from stats group by status")
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}

	session := maprclient.NewSessionState(query)
	handler := NewMaprHandler("srv1", session)
	countStorage := handlerCountStorage(t, query)

	message := strings.Join([]string{
		"ERROR",
		"2",
		countStorage + protocol.AggregateKVDelimiter + "2",
		"",
	}, protocol.AggregateDelimiter)
	if err := handler.aggregate.Aggregate(message); err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}

	handler.Shutdown()

	result, numRows, err := session.Snapshot().GlobalGroup.Result(query, 10, nil)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if numRows != 1 {
		t.Fatalf("numRows = %d, want 1", numRows)
	}
	if !strings.Contains(result, "2") {
		t.Fatalf("expected flushed aggregate row, got %q", result)
	}
}

func handlerCountStorage(t *testing.T, query *mapr.Query) string {
	t.Helper()
	for _, selectCondition := range query.Select {
		if selectCondition.Operation == mapr.Count {
			return selectCondition.FieldStorage
		}
	}
	t.Fatalf("query %q does not contain count() storage", query.RawQuery)
	return ""
}
