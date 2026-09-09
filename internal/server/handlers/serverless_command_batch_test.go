package handlers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

func TestServerlessCommandBatchDefersIdleShutdownUntilAllCommandsAreAdmitted(t *testing.T) {
	handler := newMapTestHandler(t)
	readServerMessage(t, handler.serverMessages) // Initial capability advertisement.

	var processed []string
	handler.commands["probe"] = func(_ context.Context, _ lcontext.LContext, _ int, args []string, commandFinished func()) {
		processed = append(processed, args[1])
		commandFinished()
	}

	handler.BeginCommandBatch()
	if _, err := handler.Write([]byte(encodeTestCommand("probe first"))); err != nil {
		t.Fatalf("write first command: %v", err)
	}
	select {
	case <-handler.Done():
		t.Fatal("first completed command shut down the handler before the batch was admitted")
	default:
	}

	if _, err := handler.Write([]byte(encodeTestCommand("probe second"))); err != nil {
		t.Fatalf("write second command: %v", err)
	}
	if len(processed) != 2 || processed[0] != "first" || processed[1] != "second" {
		t.Fatalf("processed commands = %v, want [first second]", processed)
	}

	if _, err := handler.Write([]byte(encodeTestCommand(protocol.ServerlessInputCompleteCommand))); err != nil {
		t.Fatalf("write input-complete marker: %v", err)
	}
	if message := readServerMessage(t, handler.serverMessages); message != ".syn close connection" {
		t.Fatalf("shutdown message = %q, want close handshake", message)
	}
	handler.handleAckCommand(3, []string{".ack", "close", "connection"})

	select {
	case <-handler.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("handler did not shut down after the complete batch became idle")
	}
}

func TestCommandBatchFinishesAggregateAfterMarkerWhenReadsAlreadyComplete(t *testing.T) {
	var batch commandBatch
	aggregate := &maprserver.Aggregate{}

	batch.begin()
	read := batch.beginRead(omode.CatClient, aggregate)
	if !read.tracked {
		t.Fatal("cat read was not accounted as one-shot batch input")
	}
	if ready := batch.completeRead(read); ready != nil {
		t.Fatal("aggregate input completed before FIFO marker")
	}

	ready := batch.complete()
	if len(ready) != 1 || ready[0] != aggregate {
		t.Fatalf("aggregates ready after marker = %v, want [%p]", ready, aggregate)
	}
}

func TestCommandBatchFinishesAggregateWhenFinalReadCompletesAfterMarker(t *testing.T) {
	var batch commandBatch
	aggregate := &maprserver.Aggregate{}

	batch.begin()
	read := batch.beginRead(omode.GrepClient, aggregate)
	if ready := batch.complete(); len(ready) != 0 {
		t.Fatalf("aggregates ready with an active read = %v, want none", ready)
	}

	if ready := batch.completeRead(read); ready != aggregate {
		t.Fatalf("aggregate ready after final read = %p, want %p", ready, aggregate)
	}
}

func TestCommandBatchMarkerAndFinalReadRaceCompletesAggregateOnce(t *testing.T) {
	for range 1000 {
		var batch commandBatch
		aggregate := &maprserver.Aggregate{}
		batch.begin()
		read := batch.beginRead(omode.CatClient, aggregate)

		ready := make(chan *maprserver.Aggregate, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for _, completed := range batch.complete() {
				ready <- completed
			}
		}()
		go func() {
			defer wg.Done()
			if completed := batch.completeRead(read); completed != nil {
				ready <- completed
			}
		}()
		wg.Wait()
		close(ready)

		var completed []*maprserver.Aggregate
		for item := range ready {
			completed = append(completed, item)
		}
		if len(completed) != 1 || completed[0] != aggregate {
			t.Fatalf("aggregate completions = %v, want exactly [%p]", completed, aggregate)
		}
	}
}

func TestCommandBatchDoesNotFinishFollowInput(t *testing.T) {
	var batch commandBatch
	aggregate := &maprserver.Aggregate{}

	batch.begin()
	if read := batch.beginRead(omode.TailClient, aggregate); read.tracked {
		t.Fatal("tail read was accounted as one-shot batch input")
	}
	if ready := batch.complete(); len(ready) != 0 {
		t.Fatalf("follow aggregates ready at batch marker = %v, want none", ready)
	}
}

func TestCommandBatchDoesNotOwnPostMarkerOrReplacementAggregate(t *testing.T) {
	var batch commandBatch
	oldAggregate := &maprserver.Aggregate{}
	replacementAggregate := &maprserver.Aggregate{}

	batch.begin()
	oldRead := batch.beginRead(omode.CatClient, oldAggregate)
	if ready := batch.complete(); len(ready) != 0 {
		t.Fatalf("aggregates ready while old read remains active = %v, want none", ready)
	}
	if read := batch.beginRead(omode.CatClient, replacementAggregate); read.tracked {
		t.Fatal("post-marker interactive read was captured by the initial batch")
	}
	if batch.ownsAggregate(replacementAggregate) {
		t.Fatal("initial batch owned a replacement aggregate")
	}
	if ready := batch.completeRead(oldRead); ready != oldAggregate {
		t.Fatalf("stale read completed aggregate %p, want old aggregate %p", ready, oldAggregate)
	}
	if batch.ownsAggregate(oldAggregate) {
		t.Fatal("initial batch did not retire after its final tracked read")
	}
}

type recordingBatchCoordinatorServer struct {
	*globCapTestServer
	coordinationCalls atomic.Int32
	coordinated       atomic.Pointer[maprserver.Aggregate]
	current           *maprserver.Aggregate
}

func (s *recordingBatchCoordinatorServer) Aggregate() *maprserver.Aggregate {
	return s.current
}

func (s *recordingBatchCoordinatorServer) coordinateAggregateInputCompletion(aggregate *maprserver.Aggregate) bool {
	s.coordinationCalls.Add(1)
	s.coordinated.Store(aggregate)
	return true
}

func TestShutdownCoordinatorDelegatesAggregateCompletionToCommandBatch(t *testing.T) {
	admittedAggregate := &maprserver.Aggregate{}
	replacementAggregate := &maprserver.Aggregate{}
	server := &recordingBatchCoordinatorServer{
		globCapTestServer: newGlobCapTestServer(1),
		current:           replacementAggregate,
	}
	coordinator := newShutdownCoordinator(server, true, admittedAggregate)

	coordinator.maybeFinishAggregateInput()

	if got := server.coordinationCalls.Load(); got != 1 {
		t.Fatalf("batch coordination calls = %d, want 1", got)
	}
	if got := server.coordinated.Load(); got != admittedAggregate {
		t.Fatalf("coordinator delegated aggregate %p, want admitted aggregate %p", got, admittedAggregate)
	}
}

type aggregateSwapReadServer struct {
	*globCapTestServer
	current atomic.Pointer[maprserver.Aggregate]
}

func (s *aggregateSwapReadServer) Aggregate() *maprserver.Aggregate {
	return s.current.Load()
}

func TestReadCommandProcessorUsesAggregateCapturedAtAdmission(t *testing.T) {
	resetServerLogger(t)
	resetCommonLogger(t)

	oldAggregate, err := maprserver.NewAggregate(
		"from STATS select count($time),$time group by $time interval 3600", "default")
	if err != nil {
		t.Fatalf("create old aggregate: %v", err)
	}
	replacementAggregate, err := maprserver.NewAggregate(
		"from STATS select count($time),$time group by $time interval 3600", "default")
	if err != nil {
		t.Fatalf("create replacement aggregate: %v", err)
	}

	readServer := &aggregateSwapReadServer{globCapTestServer: newGlobCapTestServer(1)}
	readServer.current.Store(oldAggregate)
	command := newReadCommand(readServer, omode.CatClient)
	// Model SESSION UPDATE while the admitted read is paused before processor
	// construction. The read must remain bound to the old generation.
	readServer.current.Store(replacementAggregate)

	processor := command.makeProcessor("old.log", "old.log", nil)
	if err := processor.ProcessLine(bytes.NewBufferString(testStatsLine), 1, "old.log"); err != nil {
		t.Fatalf("process old-generation line: %v", err)
	}
	if err := processor.Close(); err != nil {
		t.Fatalf("close old-generation processor: %v", err)
	}

	oldOutput := make(chan string, 4)
	oldAggregate.PrepareOutput(oldOutput)
	oldAggregate.Shutdown()
	select {
	case result := <-oldOutput:
		if !strings.Contains(result, "count($time)≔1") {
			t.Fatalf("old aggregate result = %q, want count 1", result)
		}
	default:
		t.Fatal("admitted aggregate received no result")
	}

	replacementOutput := make(chan string, 4)
	replacementAggregate.PrepareOutput(replacementOutput)
	replacementAggregate.Shutdown()
	select {
	case result := <-replacementOutput:
		t.Fatalf("replacement aggregate received stale read result %q", result)
	default:
	}
}

func TestServerlessMapBatchAcceptsReadAfterEarlierReadFullyCompletes(t *testing.T) {
	handler := newMapTestHandler(t)
	firstPath := writeTestStatsFile(t, 1)
	secondPath := writeTestStatsFile(t, 2)
	// writeTestStatsFile uses a fresh TempDir on each call, so the two paths
	// are distinct even though they share the same base name.

	commandWg := wrapHandlerCommandsForJoin(handler)
	output := &testOutput{}
	var writeMu sync.Mutex
	readerDone := startTestReader(handler, output, &writeMu)

	handler.BeginCommandBatch()
	query := "from STATS select count($time),$time group by $time interval 3600"
	writeCommand := func(command string) {
		t.Helper()
		writeMu.Lock()
		_, err := handler.Write([]byte(encodeTestCommand(command)))
		writeMu.Unlock()
		if err != nil {
			t.Fatalf("write %q: %v", command, err)
		}
	}

	writeCommand("map:plain=true:serverless=true " + query)
	writeCommand(fmt.Sprintf("cat:plain=true:serverless=true %s .", firstPath))

	// Hold the second command outside the server until the first read command
	// has returned completely. At this point the map command is the sole active
	// command. The old pending-file grace path called FinishInput here, before
	// the FIFO marker proved that the initial command stream was complete.
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, active := handler.PendingAndActive()
		aggregate := handler.getAggregate()
		if aggregate != nil && handler.commandBatch.ownsAggregate(aggregate) && pending == 0 && active == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first read did not reach map-only state: pending=%d active=%d", pending, active)
		}
		time.Sleep(time.Millisecond)
	}

	writeCommand(fmt.Sprintf("cat:plain=true:serverless=true %s .", secondPath))
	writeCommand(protocol.ServerlessInputCompleteCommand)

	select {
	case <-handler.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("serverless map batch did not shut down after the final read")
	}
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not observe EOF after serverless map shutdown")
	}
	waitForCommandJoin(t, commandWg, 5*time.Second)

	if got := output.String(); !strings.Contains(got, "count($time)≔3") {
		t.Fatalf("final aggregate omitted a read admitted after an earlier read completed: %q", got)
	}
}
