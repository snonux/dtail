package handlers

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/source"
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

// TestMaprHandlerClassifiesAuthKeyAckAsControl is a regression test for the
// dmap client feeding the server's "AUTHKEY OK" acknowledgement into the
// aggregate parser. In plain output mode the ack arrives on the wire verbatim
// (no SERVER|host| prefix), so it begins with the letter 'A' just like a real
// AGGREGATE|host|data message. Classifying on the full AggregateMessageID
// field prefix, instead of only the first byte, keeps such acks (and any
// sibling control message that merely starts with 'A') out of the aggregate
// parser, which previously logged a spurious
// "Unable to aggregate data ... expected 3 parts" error.
func TestMaprHandlerClassifiesAuthKeyAckAsControl(t *testing.T) {
	aggregate := protocol.AggregateMessageID + protocol.FieldDelimiter +
		"host1" + protocol.FieldDelimiter + "payload"

	tests := []struct {
		name          string
		message       string
		wantAggregate bool
	}{
		{
			name:          "genuine aggregate data",
			message:       aggregate,
			wantAggregate: true,
		},
		{
			name:          "plain-mode authkey ack",
			message:       "AUTHKEY OK",
			wantAggregate: false,
		},
		{
			name:          "server-prefixed authkey ack",
			message:       "SERVER" + protocol.FieldDelimiter + "host1" + protocol.FieldDelimiter + "AUTHKEY OK",
			wantAggregate: false,
		},
		{
			name:          "unrelated message starting with A",
			message:       "Application ready",
			wantAggregate: false,
		},
		{
			// Adversarial: the AGGREGATE| tag appears, but embedded in a
			// later field rather than as the leading field. Only the leading
			// tag may classify a message as aggregate data.
			message:       "SERVER" + protocol.FieldDelimiter + "host1" + protocol.FieldDelimiter + aggregate,
			name:          "embedded aggregate tag is not the leading field",
			wantAggregate: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAggregateMessage(tc.message); got != tc.wantAggregate {
				t.Fatalf("isAggregateMessage(%q) = %v, want %v",
					tc.message, got, tc.wantAggregate)
			}
		})
	}
}

// TestMaprHandlerWriteAuthKeyAckEmitsNoAggregateError feeds a plain-mode
// message stream (an "AUTHKEY OK" ack followed by a genuine aggregate message)
// through Write and asserts two things by inspecting the captured client log:
//
//  1. no spurious "Unable to aggregate data ... expected 3 parts" error is
//     emitted for the ack (the exact regression symptom); and
//  2. the genuine AGGREGATE|host|data message still produces its aggregate row.
//
// The log assertion is what makes this a real regression guard: with the old
// first-byte (message[0] == 'A') classifier the ack was fed to the aggregate
// parser and error-logged, so this test fails red against that code and green
// against the current prefix-based classifier.
func TestMaprHandlerWriteAuthKeyAckEmitsNoAggregateError(t *testing.T) {
	ensureClientStdoutLogger(t)

	query, err := mapr.NewQuery("select status,count(status) from stats group by status")
	if err != nil {
		t.Fatalf("NewQuery() error = %v", err)
	}

	session := maprclient.NewSessionState(query)
	handler := NewMaprHandler("srv1", session)
	countStorage := handlerCountStorage(t, query)

	// A genuine aggregate wire message: AGGREGATE|host|<serialized set>.
	serialized := strings.Join([]string{
		"ERROR",
		"2",
		countStorage + protocol.AggregateKVDelimiter + "2",
		"",
	}, protocol.AggregateDelimiter)
	aggregate := protocol.AggregateMessageID + protocol.FieldDelimiter +
		"host1" + protocol.FieldDelimiter + serialized

	// Plain-mode ack first, then the genuine aggregate message, each
	// terminated by the protocol message delimiter.
	var input []byte
	input = append(input, []byte("AUTHKEY OK")...)
	input = append(input, protocol.MessageDelimiter)
	input = append(input, []byte(aggregate)...)
	input = append(input, protocol.MessageDelimiter)

	logOutput := captureStdout(t, func() {
		if _, err := handler.Write(input); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		handler.Shutdown()
	})

	if strings.Contains(logOutput, "Unable to aggregate data") ||
		strings.Contains(logOutput, "expected 3 parts") {
		t.Fatalf("AUTHKEY OK ack was fed to the aggregate parser; "+
			"captured client log:\n%s", logOutput)
	}

	result, numRows, err := session.Snapshot().GlobalGroup.Result(query, 10, nil)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if numRows != 1 {
		t.Fatalf("numRows = %d, want 1 (only the genuine aggregate message)", numRows)
	}
	if !strings.Contains(result, "2") {
		t.Fatalf("expected the genuine aggregate row, got %q", result)
	}
}

// ensureClientStdoutLogger initialises the global client logger so that it
// writes plain (uncolored) lines to os.Stdout at error level. This lets tests
// capture emitted log lines via captureStdout. It is idempotent and safe under
// -count>1: dlog.Start runs at most once per process (guarded on dlog.Client),
// matching the pattern used by the mapr server tests.
func ensureClientStdoutLogger(t *testing.T) {
	t.Helper()
	if config.Common == nil {
		config.Common = &config.CommonConfig{Logger: "stdout", LogLevel: "error"}
	}
	if config.Client == nil {
		config.Client = &config.ClientConfig{}
	}
	// Force the plain log path so captured output has no color escape codes.
	config.Client.TermColorsEnable = false
	if dlog.Client == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var wg sync.WaitGroup
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Client)
	}
}

// captureStdout redirects os.Stdout to a pipe for the duration of fn and
// returns everything written to it. The stdout logger writes synchronously via
// fmt.Println, so all log lines emitted by fn are captured once fn returns.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	os.Stdout = w

	collected := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		collected <- buf.String()
	}()

	fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatalf("closing stdout pipe: %v", err)
	}
	out := <-collected
	if err := r.Close(); err != nil {
		t.Fatalf("closing stdout pipe reader: %v", err)
	}
	return out
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
