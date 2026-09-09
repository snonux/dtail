package handlers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/lcontext"
	maprserver "github.com/mimecast/dtail/internal/mapr/server"
)

func TestGracefulShutdownWaitsForAdmittedAggregatePublication(t *testing.T) {
	handler := newMapTestHandler(t)
	aggregate := newPopulatedTestAggregate(t, 1)

	commandEntered := make(chan struct{})
	publish := make(chan struct{})
	handler.commands["map"] = func(ctx context.Context, _ lcontext.LContext, _ int, _ []string, commandFinished func()) {
		close(commandEntered)
		<-publish
		messages, closeMessages := handler.newGeneratedMaprMessagesChannel(ctx, 0)
		aggregate.PrepareOutput(messages)
		handler.setAggregate(aggregate)
		go func() {
			aggregate.Start(ctx, messages)
			closeMessages()
			commandFinished()
		}()
	}

	commandDone := make(chan struct{})
	go func() {
		defer close(commandDone)
		handler.handleUserCommand(context.Background(), lcontext.LContext{}, 1, []string{"map"}, "map")
	}()
	waitForGracefulSignal(t, commandEntered, "map command initialization")

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		handler.GracefulShutdown()
	}()
	select {
	case <-shutdownDone:
		t.Fatal("graceful shutdown returned before admitted map command published its aggregate")
	case <-time.After(50 * time.Millisecond):
	}

	close(publish)
	waitForGracefulSignal(t, commandDone, "map command publication")
	waitForGracefulSignal(t, shutdownDone, "graceful shutdown")

	select {
	case message := <-handler.maprMessages:
		_, payload := decodeGeneratedMessage(message)
		if !strings.Contains(payload, "≔1") {
			t.Fatalf("final aggregate payload = %q, want one sample", payload)
		}
	default:
		t.Fatal("graceful shutdown lost the admitted aggregate's final output")
	}
}

func TestGracefulShutdownContextAbortsWhenFinalOutputCannotDrain(t *testing.T) {
	handler := newMapTestHandler(t)
	groupCount := cap(handler.maprMessages) + 32
	aggregate := newPopulatedTestAggregate(t, groupCount)
	messages, closeMessages := handler.newGeneratedMaprMessagesChannel(context.Background(), 0)
	aggregate.PrepareOutput(messages)
	handler.setAggregate(aggregate)

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		aggregate.Start(context.Background(), messages)
		closeMessages()
	}()

	drainCtx, cancelDrain := context.WithCancel(context.Background())
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		handler.GracefulShutdownContext(drainCtx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for len(handler.maprMessages) < cap(handler.maprMessages) {
		if time.Now().After(deadline) {
			t.Fatalf("final output did not fill protocol queue: len=%d cap=%d", len(handler.maprMessages), cap(handler.maprMessages))
		}
		time.Sleep(time.Millisecond)
	}
	cancelDrain()
	waitForGracefulSignal(t, shutdownDone, "canceled graceful shutdown")
	waitForGracefulSignal(t, startDone, "aggregate producer shutdown")

	if len(handler.maprMessages) != cap(handler.maprMessages) {
		t.Fatalf("protocol queue length = %d, want full capacity %d", len(handler.maprMessages), cap(handler.maprMessages))
	}
}

func newPopulatedTestAggregate(t *testing.T, groups int) *maprserver.Aggregate {
	t.Helper()

	aggregate, err := maprserver.NewAggregate(
		"from STATS select count($time),$time from - group by $time interval 3600",
		"default",
	)
	if err != nil {
		t.Fatalf("create aggregate: %v", err)
	}
	processor := maprserver.NewAggregateProcessor(aggregate, "-")
	for i := 0; i < groups; i++ {
		line := strings.Replace(testStatsLine, "1002-071143", fmt.Sprintf("1002-%06d", i), 1)
		if err := processor.ProcessLine(bytes.NewBufferString(line), uint64(i+1), "-"); err != nil {
			t.Fatalf("process aggregate line %d: %v", i, err)
		}
	}
	if err := processor.Close(); err != nil {
		t.Fatalf("close aggregate processor: %v", err)
	}
	return aggregate
}

func waitForGracefulSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
