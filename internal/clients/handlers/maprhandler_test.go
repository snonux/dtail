package handlers

import (
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/io/dlog"
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

func TestMaprHandlerWriteEmptyMessageBetweenDelimiters(t *testing.T) {
	originalLogger := dlog.Client
	dlog.Client = &dlog.DLog{}
	t.Cleanup(func() {
		dlog.Client = originalLogger
	})

	query, err := mapr.NewQuery("select status,count(status) from stats group by status")
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}

	session := maprclient.NewSessionState(query)
	handler := NewMaprHandler("srv1", session)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MaprHandler.Write panicked on empty protocol message: %v", r)
		}
	}()

	// Two consecutive MessageDelimiter bytes produce an empty message
	// between them. A leading delimiter yields an empty message too.
	// Both must be tolerated without panicking.
	input := []byte{
		protocol.MessageDelimiter,
		protocol.MessageDelimiter,
	}
	if _, err := handler.Write(input); err != nil {
		t.Fatalf("Write() error = %v", err)
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
