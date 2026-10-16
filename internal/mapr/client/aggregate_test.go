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

// serverMessages are the aggregate messages one server sends, in order.
type serverMessages struct {
	server   string
	messages []string
}

// TestAggregateMergesMessagesLackingFields verifies the client merge of
// server messages for one group where a message lacks the min/max/last/len
// fields: a later interval of the same server, or another server whose lines
// of the group had no such field. The values of the messages carrying the
// fields must survive (formerly they became 0 and ""). The servers are fed in
// the given order, so where two servers carry last()/len(), the later one's
// value must win.
func TestAggregateMergesMessagesLackingFields(t *testing.T) {
	full := aggregateTestMessage("1", "min(lat)", "5", "max(neg)", "-3",
		"last(name)", "alice", "len(name)", "alice", "count(status)", "1")
	newer := aggregateTestMessage("1", "min(lat)", "7", "max(neg)", "-1",
		"last(name)", "bob", "len(name)", "bob", "count(status)", "1")
	statusOnly := aggregateTestMessage("1", "count(status)", "1")
	fromFull := map[string]float64{"min(lat)": 5, "max(neg)": -3, "len(name)": 5, "count(status)": 2}
	tests := []struct {
		name       string
		servers    []serverMessages
		wantFloats map[string]float64
		wantName   string
	}{
		{"later interval lacks fields", []serverMessages{{"srv1", []string{full, statusOnly}}}, fromFull, "alice"},
		{"earlier interval lacks fields", []serverMessages{{"srv1", []string{statusOnly, full}}}, fromFull, "alice"},
		{"later server lacks fields", []serverMessages{{"srv1", []string{full}}, {"srv2", []string{statusOnly}}},
			fromFull, "alice"},
		{"earlier server lacks fields", []serverMessages{{"srv1", []string{statusOnly}}, {"srv2", []string{full}}},
			fromFull, "alice"},
		{"newer server wins, then one lacks fields",
			[]serverMessages{{"srv1", []string{full}}, {"srv2", []string{newer}}, {"srv3", []string{statusOnly}}},
			map[string]float64{"min(lat)": 5, "max(neg)": -1, "len(name)": 3, "count(status)": 3}, "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := mergeServerMessages(t, tt.servers)
			for key, want := range tt.wantFloats {
				if got := set.FValues[key]; got != want {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
			for _, key := range []string{"last(name)", "len(name)"} {
				if got := set.SValues[key]; got != tt.wantName {
					t.Errorf("%s = %q, want %q", key, got, tt.wantName)
				}
			}
			// Every message carries one sample.
			wantSamples := 0
			for _, server := range tt.servers {
				wantSamples += len(server.messages)
			}
			if set.Samples != wantSamples {
				t.Errorf("Samples = %d, want %d", set.Samples, wantSamples)
			}
		})
	}
}

// mergeServerMessages aggregates and flushes each server's messages into one
// session and returns the merged host1 set.
func mergeServerMessages(t *testing.T, servers []serverMessages) *mapr.AggregateSet {
	t.Helper()
	state := aggregateServerMessages(t,
		"select min(lat),max(neg),last(name),len(name),count(status) from stats group by host", servers)
	return state.Snapshot().GlobalGroup.GetSet("host1")
}

// aggregateServerMessages aggregates and flushes each server's messages, one
// server after the other, into one session of queryStr and returns it.
func aggregateServerMessages(t *testing.T, queryStr string, servers []serverMessages) *SessionState {
	t.Helper()
	state := NewSessionState(mustSessionStateQuery(t, queryStr), logging.NopLogger{})
	for _, server := range servers {
		aggregate := NewAggregate(server.server, state, logging.NopLogger{})
		for _, message := range server.messages {
			if err := aggregate.Aggregate(message); err != nil {
				t.Fatalf("Aggregate(%q) error = %v", message, err)
			}
		}
		if err := aggregate.Flush(); err != nil {
			t.Fatalf("Flush() error = %v", err)
		}
	}
	return state
}

// aggregateTestMessage builds a server aggregate message of group host1: the
// group key, the sample count, then one storage/value pair (given as
// alternating arguments) per field the group carries.
func aggregateTestMessage(samples string, storageValues ...string) string {
	parts := []string{"host1", samples}
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
