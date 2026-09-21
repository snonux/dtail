package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
	"github.com/mimecast/dtail/internal/session"
	userserver "github.com/mimecast/dtail/internal/sessionuser"
)

// capturedBatch records a batch handed to enqueueOutput together with a copy
// of its bytes at hand-over time.
type capturedBatch struct {
	data     []byte
	snapshot []byte
}

func newCapturingNetworkWriter(t *testing.T) (*NetworkWriter, *[]capturedBatch) {
	t.Helper()
	var batches []capturedBatch
	writer := NewNetworkWriter(context.Background(), nil, nil, "testhost", true, false,
		0, nil, handlerTestLogger)
	writer.enqueueOutput = func(_ context.Context, _ uint64, data []byte, _ func() uint64) error {
		batches = append(batches, capturedBatch{data: data, snapshot: append([]byte(nil), data...)})
		return nil
	}
	return writer, &batches
}

// sharesBacking reports whether a and b are views of the same backing array
// anywhere within their capacities.
func sharesBacking(a, b []byte) bool {
	if cap(a) == 0 || cap(b) == 0 {
		return false
	}
	aStart := uintptr(unsafe.Pointer(unsafe.SliceData(a)))
	bStart := uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	return aStart < bStart+uintptr(cap(b)) && bStart < aStart+uintptr(cap(a))
}

func writeLinesUntil(t *testing.T, writer *NetworkWriter, batches *[]capturedBatch, want int, prefix byte) {
	t.Helper()
	line := bytes.Repeat([]byte{prefix}, 127)
	for i := 0; len(*batches) < want; i++ {
		if i > 10000 {
			t.Fatalf("no batch %d after %d lines", want, i)
		}
		if err := writer.WriteLineData(line, uint64(i), "app.log"); err != nil {
			t.Fatalf("WriteLineData: %v", err)
		}
	}
}

// A full batch is handed over without copying, and the writer's next batch
// must never write into the handed-over bytes.
func TestNetworkWriterHandsOverFullBatchWithoutAliasing(t *testing.T) {
	writer, batches := newCapturingNetworkWriter(t)

	writeLinesUntil(t, writer, batches, 1, 'a')
	first := (*batches)[0]
	if len(first.data) < networkWriterBufferSize {
		t.Fatalf("first batch = %d bytes, want at least %d", len(first.data), networkWriterBufferSize)
	}
	if got, want := cap(first.data), networkWriterBatchCapacity(networkWriterBufferSize); got != want {
		t.Fatalf("handed-over batch capacity = %d, want the reserved %d (no regrow)", got, want)
	}

	writer.mutex.Lock()
	if writer.writeBuf.Cap() != 0 {
		writer.mutex.Unlock()
		t.Fatalf("writer kept a %d byte buffer after handing its batch over", writer.writeBuf.Cap())
	}
	writer.mutex.Unlock()

	// Fill a second batch, then a partial one that stays in the writer.
	writeLinesUntil(t, writer, batches, 2, 'b')
	if err := writer.WriteLineData([]byte("tail"), 1, "app.log"); err != nil {
		t.Fatalf("WriteLineData: %v", err)
	}

	if !bytes.Equal(first.data, first.snapshot) {
		t.Fatal("handed-over batch changed after later writes")
	}
	second := (*batches)[1]
	if sharesBacking(first.data, second.data) {
		t.Fatal("second batch shares backing memory with the handed-over first batch")
	}
	writer.mutex.Lock()
	pending := writer.writeBuf.Bytes()
	aliased := sharesBacking(pending[:cap(pending)], first.data) ||
		sharesBacking(pending[:cap(pending)], second.data)
	writer.mutex.Unlock()
	if aliased {
		t.Fatal("writer buffer aliases a handed-over batch")
	}
	for _, b := range bytes.Split(bytes.TrimSuffix(second.data, []byte{protocol.MessageDelimiter}),
		[]byte{protocol.MessageDelimiter}) {
		if len(b) != 127 || b[0] != 'b' {
			t.Fatalf("second batch holds unexpected record %q", b)
		}
	}
}

// A small flush is copied and the writer keeps (and reuses) its buffer; the
// copy must not alias that buffer, or the next batch would overwrite it.
func TestNetworkWriterCopiesSmallFlushAndKeepsBuffer(t *testing.T) {
	writer, batches := newCapturingNetworkWriter(t)

	if err := writer.WriteLineData([]byte("first"), 1, "app.log"); err != nil {
		t.Fatalf("WriteLineData: %v", err)
	}
	writer.mutex.Lock()
	reserved := writer.writeBuf.Bytes()[:writer.writeBuf.Cap()]
	writer.mutex.Unlock()
	if got, want := cap(reserved), networkWriterBatchCapacity(networkWriterBufferSize); got != want {
		t.Fatalf("reserved batch capacity = %d, want %d", got, want)
	}

	if err := writer.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(*batches) != 1 {
		t.Fatalf("batches after flush = %d, want 1", len(*batches))
	}
	flushed := (*batches)[0]
	if sharesBacking(flushed.data, reserved) {
		t.Fatal("small flush handed over the writer's own buffer")
	}
	if cap(flushed.data) >= outputAdoptMinBytes {
		t.Fatalf("small flush capacity = %d, want an exact-size copy", cap(flushed.data))
	}

	if err := writer.WriteLineData([]byte("second"), 2, "app.log"); err != nil {
		t.Fatalf("WriteLineData: %v", err)
	}
	writer.mutex.Lock()
	reused := sharesBacking(writer.writeBuf.Bytes(), reserved)
	writer.mutex.Unlock()
	if !reused {
		t.Fatal("writer did not reuse its buffer after a small flush")
	}
	if !bytes.Equal(flushed.data, flushed.snapshot) {
		t.Fatalf("flushed batch changed to %q after the next write", flushed.data)
	}
}

func newHandoverTestManager(maxBytes int) *outputManager {
	manager := &outputManager{}
	manager.configure(outputManagerConfig{bufferMaxBytes: maxBytes, readRetryInterval: time.Millisecond},
		handlerTestLogger)
	manager.enable()
	return manager
}

// largePayloadCapacity matches a fresh 72 KiB NetworkWriter batch buffer.
const largePayloadCapacity = 72 * 1024

func largePayload(length int, fill byte) []byte {
	payload := make([]byte, length, largePayloadCapacity)
	for i := range payload {
		payload[i] = fill
	}
	return payload
}

func managerAccounting(manager *outputManager) (retained, buffered, entries int) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.retainedBytes, manager.bufferedBytes, manager.bufferedEntries
}

func drainManager(t *testing.T, manager *outputManager, readSize int) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, readSize)
	for manager.bufferedLen() > 0 {
		n, handled := manager.tryRead(buf, &userserver.User{Name: "handover-test"}, nil)
		if !handled || n == 0 {
			t.Fatalf("drain stalled: handled=%v n=%d", handled, n)
		}
		out = append(out, buf[:n]...)
	}
	return out
}

func TestOutputManagerAdoptionAccounting(t *testing.T) {
	const (
		kib       = 1024
		length    = 40 * kib
		capacity  = largePayloadCapacity
		smallSize = outputAdoptMinBytes - 1
	)
	tests := []struct {
		name         string
		maxBytes     int
		payloads     [][]byte
		wantAdopted  []bool
		wantRetained int
		wantEntries  int
	}{
		{
			name:         "large payload adopted and charged its capacity",
			maxBytes:     128 * kib,
			payloads:     [][]byte{largePayload(length, 'a')},
			wantAdopted:  []bool{true},
			wantRetained: capacity,
			wantEntries:  1,
		},
		{
			name:         "capacity over the remaining budget falls back to an exact copy",
			maxBytes:     120 * kib,
			payloads:     [][]byte{largePayload(length, 'a'), largePayload(length, 'b')},
			wantAdopted:  []bool{true, false},
			wantRetained: capacity + length,
			wantEntries:  2,
		},
		{
			name:         "capacity over the whole cap is copied, not stranded",
			maxBytes:     64 * kib,
			payloads:     [][]byte{largePayload(length, 'a')},
			wantAdopted:  []bool{false},
			wantRetained: length,
			wantEntries:  1,
		},
		{
			name:         "same-generation large payloads get their own descriptors",
			maxBytes:     256 * kib,
			payloads:     [][]byte{largePayload(length, 'a'), largePayload(length, 'b')},
			wantAdopted:  []bool{true, true},
			wantRetained: 2 * capacity,
			wantEntries:  2,
		},
		{
			name:         "payload below the adoption size is copied",
			maxBytes:     256 * kib,
			payloads:     [][]byte{largePayload(smallSize, 'a')},
			wantAdopted:  []bool{false},
			wantRetained: smallSize,
			wantEntries:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newHandoverTestManager(tt.maxBytes)
			var want []byte
			for _, payload := range tt.payloads {
				want = append(want, payload...)
				if err := manager.enqueue(context.Background(), 1, payload, nil); err != nil {
					t.Fatalf("enqueue: %v", err)
				}
			}
			manager.mu.Lock()
			for i, entry := range manager.queue {
				if got := sharesBacking(entry.payload, tt.payloads[i]); got != tt.wantAdopted[i] {
					manager.mu.Unlock()
					t.Fatalf("payload %d adopted = %v, want %v", i, got, tt.wantAdopted[i])
				}
				if entry.retainedBytes != cap(entry.payload) {
					manager.mu.Unlock()
					t.Fatalf("payload %d charged %d bytes for a %d byte allocation", i,
						entry.retainedBytes, cap(entry.payload))
				}
			}
			manager.mu.Unlock()
			retained, buffered, entries := managerAccounting(manager)
			if retained != tt.wantRetained || entries != tt.wantEntries || buffered != len(want) {
				t.Fatalf("retained/buffered/entries = %d/%d/%d, want %d/%d/%d",
					retained, buffered, entries, tt.wantRetained, len(want), tt.wantEntries)
			}
			if retained > tt.maxBytes {
				t.Fatalf("retained %d bytes over the %d byte cap", retained, tt.maxBytes)
			}

			if got := drainManager(t, manager, 3000); !bytes.Equal(got, want) {
				t.Fatalf("drained %d bytes differ from the %d enqueued", len(got), len(want))
			}
			if retained, buffered, entries := managerAccounting(manager); retained != 0 || buffered != 0 || entries != 0 {
				t.Fatalf("after drain retained/buffered/entries = %d/%d/%d, want 0/0/0", retained, buffered, entries)
			}
		})
	}
}

// An adopted payload's capacity counts against the cap until the reader has
// consumed all of it, and backpressure releases exactly when it is gone.
func TestOutputManagerAdoptedCapacityBackpressure(t *testing.T) {
	const kib = 1024
	manager := newHandoverTestManager(100 * kib)
	first := largePayload(40*kib, 'a')
	if err := manager.enqueue(context.Background(), 1, first, nil); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}

	// 72 KiB retained: 40 KiB fits neither adopted (72) nor copied (40).
	done := make(chan error, 1)
	go func() {
		done <- manager.enqueue(context.Background(), 1, largePayload(40*kib, 'b'), nil)
	}()
	select {
	case err := <-done:
		t.Fatalf("enqueue beyond the cap returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	buf := make([]byte, 40*kib-1)
	if n, handled := manager.tryRead(buf, &userserver.User{Name: "handover-test"}, nil); !handled || n != len(buf) {
		t.Fatalf("partial read = handled %v n %d", handled, n)
	}
	select {
	case err := <-done:
		t.Fatalf("enqueue admitted while the adopted allocation was still retained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if retained, _, _ := managerAccounting(manager); retained != 72*kib {
		t.Fatalf("retained during partial drain = %d, want %d", retained, 72*kib)
	}

	if n, handled := manager.tryRead(buf, &userserver.User{Name: "handover-test"}, nil); !handled || n != 1 {
		t.Fatalf("final read = handled %v n %d", handled, n)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked enqueue: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue not released after the adopted allocation drained")
	}
	if retained, buffered, _ := managerAccounting(manager); retained != 72*kib || buffered != 40*kib {
		t.Fatalf("retained/buffered = %d/%d, want %d/%d", retained, buffered, 72*kib, 40*kib)
	}
}

// End to end through the production writer and manager: the cap holds at
// every point, and the bytes read equal the bytes written.
func TestNetworkWriterHandoverRespectsCapEndToEnd(t *testing.T) {
	const maxBytes = 3 * 72 * 1024
	manager := newHandoverTestManager(maxBytes)
	writer := NewNetworkWriter(context.Background(), nil, nil, "testhost", true, false,
		0, nil, handlerTestLogger)
	writer.enqueueOutput = manager.enqueue

	var want bytes.Buffer
	writeDone := make(chan error, 1)
	go func() {
		for i := 0; i < 20000; i++ {
			line := []byte(fmt.Sprintf("line-%06d-%s", i, strings.Repeat("x", i%300)))
			want.Write(line)
			want.WriteByte(protocol.MessageDelimiter)
			if err := writer.WriteLineData(line, uint64(i), "app.log"); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- writer.Flush()
	}()

	var got []byte
	buf := make([]byte, 7919)
	var writeErr error
	finished := false
	for !finished || manager.bufferedLen() > 0 {
		if !finished {
			select {
			case writeErr = <-writeDone:
				finished = true
			default:
			}
		}
		if retained, _, _ := managerAccounting(manager); retained > maxBytes {
			t.Fatalf("retained %d bytes over the %d byte cap", retained, maxBytes)
		}
		n, _ := manager.tryRead(buf, &userserver.User{Name: "handover-test"}, nil)
		got = append(got, buf[:n]...)
	}
	if writeErr != nil {
		t.Fatalf("writer: %v", writeErr)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("read %d bytes, want %d identical bytes", len(got), want.Len())
	}
}

// After a reload, a reader that has observed the new generation must not
// deliver records of the old one (except the rest of a record whose prefix was
// already sent), while writers of both generations run concurrently.
func TestStaleGenerationDroppedUnderConcurrentReload(t *testing.T) {
	var state sessionCommandState
	state.mu.Lock()
	state.active = true
	state.generation.Store(1)
	state.mu.Unlock()

	manager := newHandoverTestManager(4 * 72 * 1024)
	newWriter := func(generation uint64) *NetworkWriter {
		w := NewNetworkWriter(context.Background(), nil, nil, "testhost", true, false,
			generation, state.currentGeneration, handlerTestLogger)
		w.enqueueOutput = manager.enqueue
		return w
	}

	var flipped atomic.Bool
	stop := make(chan struct{})
	var writers sync.WaitGroup
	writeGeneration := func(generation uint64, lines int, tag string) {
		defer writers.Done()
		w := newWriter(generation)
		for i := 0; i < lines && state.currentGeneration() == generation; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := w.WriteLineData([]byte(fmt.Sprintf("%s-%06d-%s", tag, i, strings.Repeat("y", 100))),
				uint64(i), "app.log"); err != nil {
				t.Errorf("generation %d write: %v", generation, err)
				return
			}
		}
		if err := w.Flush(); err != nil {
			t.Errorf("generation %d flush: %v", generation, err)
		}
	}

	writers.Add(1)
	go writeGeneration(1, 1<<30, "g1") // Runs until it becomes stale.

	shouldDrop := func(generation uint64) bool {
		active := state.currentGeneration()
		return generation != 0 && active != 0 && generation != active
	}
	buf := make([]byte, 5003)
	var before, after []byte
	const gen2Lines = 20000
	gen2Started := false
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			close(stop)
			t.Fatalf("timed out; %d bytes read after the reload", len(after))
		default:
		}
		observed := flipped.Load()
		n, _ := manager.tryRead(buf, &userserver.User{Name: "reload-test"}, shouldDrop)
		if observed {
			after = append(after, buf[:n]...)
		} else {
			before = append(before, buf[:n]...)
		}
		if !gen2Started && len(before) > 512*1024 {
			state.mu.Lock()
			state.generation.Store(2)
			state.mu.Unlock()
			flipped.Store(true)
			gen2Started = true
			writers.Add(1)
			go writeGeneration(2, gen2Lines, "g2")
		}
		if gen2Started && bytes.Count(after, []byte("g2-")) == gen2Lines {
			break
		}
	}
	close(stop)
	writers.Wait()
	for manager.bufferedLen() > 0 {
		n, _ := manager.tryRead(buf, &userserver.User{Name: "reload-test"}, shouldDrop)
		after = append(after, buf[:n]...)
	}

	if !bytes.Contains(before, []byte("g1-")) {
		t.Fatal("no generation 1 output before the reload")
	}
	// At most the unfinished record of generation 1 may be completed first.
	records := bytes.Split(after, []byte{protocol.MessageDelimiter})
	if len(records) > 0 && !bytes.HasPrefix(records[0], []byte("g2-")) {
		records = records[1:]
	}
	for i, record := range records {
		if len(record) > 0 && !bytes.HasPrefix(record, []byte("g2-")) {
			t.Fatalf("record %d after the reload is stale: %.40q", i, record)
		}
	}
	if retained, buffered, entries := managerAccounting(manager); buffered != 0 || entries != 0 || retained != 0 {
		t.Fatalf("after drain retained/buffered/entries = %d/%d/%d, want 0/0/0", retained, buffered, entries)
	}
}

// update must publish the new generation before it cancels the previous
// generation's context: a stale command that stops because of the
// cancellation must already see its generation as stale.
func TestSessionUpdatePublishesGenerationBeforeCancellingPrevious(t *testing.T) {
	handler, recorder := newSessionDispatchTestHandler("session-generation-order-user")
	readServerMessage(t, handler.serverMessages)
	t.Cleanup(func() {
		handler.sessionState.mu.Lock()
		cancel := handler.sessionState.cancel
		handler.sessionState.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		recorder.wg.Wait()
	})

	payload := mustSessionPayload(t, session.Spec{Mode: omode.TailClient, Files: []string{"/var/log/a.log"}})
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "START", payload}, func() {})
	readServerMessage(t, handler.serverMessages)
	first := recorder.waitForStart(t)

	seen := make(chan uint64, 1)
	go func() {
		<-first.ctx.Done()
		seen <- handler.sessionState.currentGeneration()
	}()
	handler.handleSessionCommand(context.Background(), lcontext.LContext{}, 3,
		[]string{"SESSION", "UPDATE", payload}, func() {})
	readServerMessage(t, handler.serverMessages)
	recorder.waitForStart(t)

	select {
	case generation := <-seen:
		if generation != 2 {
			t.Fatalf("generation seen after cancellation = %d, want 2", generation)
		}
	case <-time.After(time.Second):
		t.Fatal("previous generation context was not cancelled")
	}
}

var errHandoverTestSentinel = errors.New("sentinel")

// A batch handed over to a failing enqueue is dropped, not reused: the next
// batch must get a fresh allocation.
func TestNetworkWriterDoesNotReuseBatchAfterFailedEnqueue(t *testing.T) {
	var handed [][]byte
	writer := NewNetworkWriter(context.Background(), nil, nil, "testhost", true, false,
		0, nil, handlerTestLogger)
	writer.enqueueOutput = func(_ context.Context, _ uint64, data []byte, _ func() uint64) error {
		handed = append(handed, data)
		return errHandoverTestSentinel
	}
	line := bytes.Repeat([]byte{'z'}, 127)
	for i := 0; len(handed) < 2; i++ {
		err := writer.WriteLineData(line, uint64(i), "app.log")
		if err != nil && !errors.Is(err, errHandoverTestSentinel) {
			t.Fatalf("WriteLineData: %v", err)
		}
		if i > 10000 {
			t.Fatal("no second batch")
		}
	}
	if sharesBacking(handed[0], handed[1]) {
		t.Fatal("writer reused the backing of a batch it had handed over")
	}
}

// BenchmarkNetworkWriterToOutputManager measures the server-mode payload path
// from NetworkWriter.WriteLineData through the output queue to the session
// reader, per 128-byte line, with generation gating active.
func BenchmarkNetworkWriterToOutputManager(b *testing.B) {
	var state sessionCommandState
	state.generation.Store(1)
	manager := &outputManager{}
	manager.configure(outputManagerConfig{}, handlerTestLogger)
	manager.enable()
	writer := NewNetworkWriter(context.Background(), nil, nil, "testhost", true, false,
		1, state.currentGeneration, handlerTestLogger)
	writer.enqueueOutput = manager.enqueue
	shouldDrop := func(generation uint64) bool { return generation != state.currentGeneration() }
	reader := &userserver.User{Name: "bench"}
	line := bytes.Repeat([]byte{'x'}, 127)
	buf := make([]byte, 32*1024)

	b.SetBytes(int64(len(line) + 1))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writer.WriteLineData(line, uint64(i), "app.log"); err != nil {
			b.Fatal(err)
		}
		if i%512 == 511 {
			for {
				if n, _ := manager.tryRead(buf, reader, shouldDrop); n == 0 {
					break
				}
			}
		}
	}
}
