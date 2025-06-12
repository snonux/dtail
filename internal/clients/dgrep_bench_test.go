package clients

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/io/signal"
	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/user"
)

func ensureTestFile10MBForDgrep(b *testing.B, filename string) {
	const line = "2023-01-01 12:00:00 INFO [service] Processing request id=12345 user=john.doe@example.com action=login status=success duration=150ms\n"
	const targetSize = 10 * 1024 * 1024 // 10MB
	if fi, err := os.Stat(filename); err == nil && fi.Size() >= targetSize {
		return // File already exists and is large enough
	}
	f, err := os.Create(filename)
	if err != nil {
		b.Fatalf("failed to create test file: %v", err)
	}
	defer f.Close()
	written := int64(0)
	for written < targetSize {
		n, err := f.WriteString(line)
		if err != nil {
			b.Fatalf("failed to write to test file: %v", err)
		}
		written += int64(n)
	}
}

func BenchmarkDGrepFile10MBNoMatch(b *testing.B) {
	const filename = "testfile_10mb_dgrep.log"
	b.Log("Ensuring test file exists...")
	ensureTestFile10MBForDgrep(b, filename)
	b.Log("Test file ensured.")

	// Setup args similar to dgrep main
	args := config.Args{
		RegexStr:           "NONEXISTENTPATTERN12345",  // Pattern that won't match anything
		What:               filename,
		NoColor:            true,
		Plain:              true,
		Quiet:              true,
		ConnectionsPerCPU:  config.DefaultConnectionsPerCPU,
		SSHPort:            config.DefaultSSHPort,
		Logger:             "none",   // Use none logger to suppress output
		LogLevel:           "TRACE",
		UserName:           user.Name(),
		ConfigFile:         "none",
	}

	// Initialize config first
	config.Setup(source.Client, &args, []string{})

	// Initialize logging context only if not already started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	
	// Only start dlog if it hasn't been started already
	if dlog.Client == nil {
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Client)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, err := NewGrepClient(args)
		if err != nil {
			b.Fatalf("failed to create grep client: %v", err)
		}

		// Run the grep operation
		client.Start(ctx, signal.InterruptCh(ctx))
	}
	b.StopTimer()

	cancel()
	wg.Wait()
	
	// Cleanup
	os.Remove(filename)
}

func BenchmarkDGrepFile10MBWithMatches(b *testing.B) {
	const filename = "testfile_10mb_dgrep.log"
	b.Log("Ensuring test file exists...")
	ensureTestFile10MBForDgrep(b, filename)
	b.Log("Test file ensured.")

	// Setup args similar to dgrep main
	args := config.Args{
		RegexStr:           "user=john.doe",  // Pattern that will match every line
		What:               filename,
		NoColor:            true,
		Plain:              true,
		Quiet:              true,
		ConnectionsPerCPU:  config.DefaultConnectionsPerCPU,
		SSHPort:            config.DefaultSSHPort,
		Logger:             "none",   // Use none logger to suppress output
		LogLevel:           "TRACE",
		UserName:           user.Name(),
		ConfigFile:         "none",
	}

	// Initialize config first
	config.Setup(source.Client, &args, []string{})

	// Initialize logging context only if not already started
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	
	// Only start dlog if it hasn't been started already
	if dlog.Client == nil {
		wg.Add(1)
		dlog.Start(ctx, &wg, source.Client)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, err := NewGrepClient(args)
		if err != nil {
			b.Fatalf("failed to create grep client: %v", err)
		}

		// Run the grep operation
		client.Start(ctx, signal.InterruptCh(ctx))
	}
	b.StopTimer()

	cancel()
	wg.Wait()
	
	// Cleanup
	os.Remove(filename)
}