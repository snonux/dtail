package fs

import (
	"context"
	"os"
	"testing"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

func ensureTestFile100MBForCat(b *testing.B, filename string) {
	const line = "test line 1\n"
	const targetSize = 100 * 1024 * 1024 // 100MB
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

func BenchmarkCatFile100MB(b *testing.B) {
	const filename = "testfile_100mb.log"
	const globID = "test"
	b.Log("Ensuring test file exists...")
	ensureTestFile100MBForCat(b, filename)
	b.Log("Test file ensured.")
	serverMessages := make(chan string, 100)

	b.Log("Created serverMessages channel.")
	catFile := NewCatFile(filename, globID, serverMessages)
	b.Log("Created CatFile instance.")
	ltx := lcontext.LContext{}
	noopRegex, _ := regex.New("", regex.Noop)
	ctx := context.Background()
	config.Server = &config.ServerConfig{} // Initialize config.Server
	b.Log("Initialized context and config.Server.")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.Logf("Starting iteration %d...", i)
		go func() {
			b.Log("Goroutine started to read messages.")
			for msg := range serverMessages {
				b.Logf("Received message: %s", msg)
			}
			b.Log("Goroutine finished reading messages.")
		}()
		b.Log("Started goroutine to read messages.")
		b.Log("Calling catFile.Start...")
		catFile.Start(ctx, ltx, nil, noopRegex)
		b.Log("catFile.Start completed.")
	}
	b.StopTimer()
	close(serverMessages)
	b.Log("Benchmark completed.")
}
