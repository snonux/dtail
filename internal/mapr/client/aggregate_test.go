package client

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/protocol"
)

func TestAggregateResetsPendingLocalStateOnGenerationChange(t *testing.T) {
	query := mustSessionStateQuery(t, "select status,count(status) from stats group by status")
	state := NewSessionState(query, logging.NopLogger{})
	aggregate := NewAggregate("srv1", state, logging.NopLogger{})
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
	state := NewSessionState(query, logging.NopLogger{})
	aggregate := NewAggregate("srv1", state, logging.NopLogger{})

	if err := aggregate.Aggregate("broken"); err == nil {
		t.Fatalf("expected Aggregate() to reject malformed messages")
	}
}

func TestAggregateFlushMergesPendingLocalState(t *testing.T) {
	query := mustSessionStateQuery(t, "select status,count(status) from stats group by status")
	state := NewSessionState(query, logging.NopLogger{})
	aggregate := NewAggregate("srv1", state, logging.NopLogger{})
	countStorage := aggregateCountStorage(t, query)

	set := aggregate.group.GetSet("ERROR")
	set.Samples = 3
	set.FValues[countStorage] = 3

	if err := aggregate.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	result, numRows, err := state.Snapshot().GlobalGroup.Result(query, 10, nil)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if numRows != 1 {
		t.Fatalf("numRows = %d, want 1", numRows)
	}
	if !strings.Contains(result, "3") {
		t.Fatalf("expected flushed aggregate row, got %q", result)
	}
}

func TestAggregateAndFlushAreSafeConcurrently(t *testing.T) {
	query := mustSessionStateQuery(t, "select status,count(status) from stats group by status")
	state := NewSessionState(query, logging.NopLogger{})
	aggregate := NewAggregate("srv1", state, logging.NopLogger{})
	countStorage := aggregateCountStorage(t, query)

	const messageCount = 1000
	message := strings.Join([]string{
		"ERROR",
		"1",
		countStorage + protocol.AggregateKVDelimiter + "1",
		"",
	}, protocol.AggregateDelimiter)

	start := make(chan struct{})
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range messageCount {
			if err := aggregate.Aggregate(message); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range messageCount {
			if err := aggregate.Flush(); err != nil {
				errors <- err
				return
			}
		}
	}()

	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent aggregate operation failed: %v", err)
	}
	if err := aggregate.Flush(); err != nil {
		t.Fatalf("final Flush() error = %v", err)
	}

	result, numRows, err := state.Snapshot().GlobalGroup.Result(query, 10, nil)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if numRows != 1 {
		t.Fatalf("numRows = %d, want 1", numRows)
	}
	fields := strings.Fields(result)
	if got, want := fields[len(fields)-1], strconv.Itoa(messageCount); got != want {
		t.Fatalf("aggregate count = %s, want %s; result:\n%s", got, want, result)
	}
}

// TestAggregateMergesMessagesLackingFields verifies the client merge of
// server messages for one group where a message lacks the min/max/last/len
// fields: a later interval of the same server, or another server whose lines
// of the group had no such field. The values of the messages carrying the
// fields must survive (formerly they became 0 and "").
func TestAggregateMergesMessagesLackingFields(t *testing.T) {
	full := aggregateTestMessage("host1", "1", "min(lat)", "5", "max(neg)", "-3",
		"last(name)", "alice", "len(name)", "alice", "count(status)", "1")
	statusOnly := aggregateTestMessage("host1", "1", "count(status)", "1")
	tests := []struct {
		name     string
		messages map[string][]string
	}{
		{"later interval lacks fields", map[string][]string{"srv1": {full, statusOnly}}},
		{"earlier interval lacks fields", map[string][]string{"srv1": {statusOnly, full}}},
		{"other server lacks fields", map[string][]string{"srv1": {full}, "srv2": {statusOnly}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := mustSessionStateQuery(t,
				"select min(lat),max(neg),last(name),len(name),count(status) from stats group by host")
			state := NewSessionState(query, logging.NopLogger{})
			for server, messages := range tt.messages {
				aggregate := NewAggregate(server, state, logging.NopLogger{})
				for _, message := range messages {
					if err := aggregate.Aggregate(message); err != nil {
						t.Fatalf("Aggregate(%q) error = %v", message, err)
					}
				}
				if err := aggregate.Flush(); err != nil {
					t.Fatalf("Flush() error = %v", err)
				}
			}
			set := state.Snapshot().GlobalGroup.GetSet("host1")
			wantFloats := map[string]float64{"min(lat)": 5, "max(neg)": -3, "len(name)": 5, "count(status)": 2}
			for key, want := range wantFloats {
				if got := set.FValues[key]; got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
			for _, key := range []string{"last(name)", "len(name)"} {
				if got := set.SValues[key]; got != "alice" {
					t.Errorf("%s = %q, want %q", key, got, "alice")
				}
			}
			if set.Samples != 2 {
				t.Errorf("Samples = %d, want 2", set.Samples)
			}
		})
	}
}

// aggregateTestMessage builds a server aggregate message: the group key, the
// sample count, then one storage/value pair (given as alternating arguments)
// per field the group carries.
func aggregateTestMessage(groupKey, samples string, storageValues ...string) string {
	parts := []string{groupKey, samples}
	for i := 0; i+1 < len(storageValues); i += 2 {
		parts = append(parts, storageValues[i]+protocol.AggregateKVDelimiter+storageValues[i+1])
	}
	return strings.Join(append(parts, ""), protocol.AggregateDelimiter)
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
