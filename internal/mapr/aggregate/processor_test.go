package aggregate

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/logging"
)

const processorTestQuery = `from STATS select count($line) group by host`

// samplesOf returns the sample count aggregated for group so far.
func samplesOf(aggregate *Aggregate, group string) int {
	aggregate.serializer.groupMu.Lock()
	defer aggregate.serializer.groupMu.Unlock()
	if set, ok := aggregate.serializer.groupSets[group]; ok {
		return set.Samples
	}
	return 0
}

func feedLines(t *testing.T, processor *Processor, lines ...string) {
	t.Helper()
	for i, line := range lines {
		if err := processor.ProcessLine(bytes.NewBufferString(line), uint64(i+1), "test"); err != nil {
			t.Fatalf("ProcessLine() error = %v", err)
		}
	}
}

// TestProcessorFlushDrainsEveryPartialBatch pins the follow-mode contract: a
// follow reader flushes after every read, and every one of those flushes, not
// only the first, must hand the partial batch to the aggregate.
func TestProcessorFlushDrainsEveryPartialBatch(t *testing.T) {
	aggregate, err := newAggregateFromTextForTest(processorTestQuery, logging.NopLogger{})
	if err != nil {
		t.Fatalf("Failed to create aggregate: %v", err)
	}
	processor := NewProcessor(aggregate, "follow")

	feedLines(t, processor, retentionLines[0], retentionLines[2])
	if got := samplesOf(aggregate, "alpha"); got != 0 {
		t.Fatalf("a partial batch reached the aggregate before Flush: %d samples", got)
	}
	for round, want := range []int{2, 3, 4} {
		if round > 0 {
			feedLines(t, processor, retentionLines[0])
		}
		if err := processor.Flush(); err != nil {
			t.Fatalf("Flush() error = %v", err)
		}
		if got := samplesOf(aggregate, "alpha"); got != want {
			t.Fatalf("flush %d: alpha has %d samples, want %d", round+1, got, want)
		}
	}

	if err := processor.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := aggregate.filesProcessed.Load(); got != 1 {
		t.Errorf("filesProcessed = %d after several flushes, want 1", got)
	}
	if got := aggregate.linesProcessed.Load(); got != 4 {
		t.Errorf("linesProcessed = %d, want 4", got)
	}
}

// TestProcessorPartialBatchReachesFinalSerialization covers graceful shutdown
// racing a reader: lines a processor accepted before shutdown began are still
// batched when the aggregate stops, and Close must aggregate them before the
// final serialization, as the former shared batch drain did.
func TestProcessorPartialBatchReachesFinalSerialization(t *testing.T) {
	aggregate, err := newAggregateFromTextForTest(processorTestQuery, logging.NopLogger{})
	if err != nil {
		t.Fatalf("Failed to create aggregate: %v", err)
	}
	messages := make(chan string, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		aggregate.Start(ctx, messages)
	}()
	waitForAggregateStart(t, aggregate)

	processor := NewProcessor(aggregate, "partial")
	feedLines(t, processor, retentionLines...)

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		aggregate.Shutdown(context.Background())
	}()
	waitForAggregateCondition(t, 2*time.Second, "shutdown did not stop the aggregate",
		aggregate.stopping)

	// The reader notices the cancellation and closes its processor.
	if err := processor.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not complete after the processor closed")
	}
	cancel()
	<-startDone
	close(messages)

	var results []string
	for message := range messages {
		results = append(results, message)
	}
	joined := strings.Join(results, "\n")
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "beta") {
		t.Fatalf("final serialization %q lacks the batched lines", joined)
	}
}

// TestProcessorAbortDiscardsPartialBatch checks that output-free termination
// does not aggregate lines still batched in a processor.
func TestProcessorAbortDiscardsPartialBatch(t *testing.T) {
	aggregate, err := newAggregateFromTextForTest(processorTestQuery, logging.NopLogger{})
	if err != nil {
		t.Fatalf("Failed to create aggregate: %v", err)
	}
	processor := NewProcessor(aggregate, "abort")
	feedLines(t, processor, retentionLines...)

	aggregate.Abort()
	if err := processor.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := aggregate.countGroups(); got != 0 {
		t.Errorf("aborted aggregate holds %d groups, want 0", got)
	}
	if got := len(processor.batch.pending()); got != 0 {
		t.Errorf("Close left %d lines batched", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	aggregate.AbortAndWait(ctx)
	if ctx.Err() != nil {
		t.Fatal("AbortAndWait did not join the closed processor")
	}
}
