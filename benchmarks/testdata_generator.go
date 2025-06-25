package benchmarks

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DataDog/zstd"
)

// FileSize represents the size category of test files
type FileSize int

const (
	Small  FileSize = 10 * 1024 * 1024        // 10MB
	Medium FileSize = 100 * 1024 * 1024       // 100MB
	Large  FileSize = 1024 * 1024 * 1024      // 1GB
)

func (fs FileSize) String() string {
	switch fs {
	case Small:
		return "10MB"
	case Medium:
		return "100MB"
	case Large:
		return "1GB"
	default:
		return fmt.Sprintf("%dB", fs)
	}
}

// LogFormat represents different log format types
type LogFormat int

const (
	SimpleLogFormat LogFormat = iota
	MapReduceLogFormat
	MixedLogFormat
)

// CompressionType represents file compression options
type CompressionType int

const (
	NoCompression CompressionType = iota
	GzipCompression
	ZstdCompression
)

// TestDataConfig configures test data generation
type TestDataConfig struct {
	Size          FileSize
	Format        LogFormat
	Compression   CompressionType
	LineVariation int    // Percentage of unique lines (0-100)
	Pattern       string // Pattern to include for grep testing
	PatternRate   int    // Percentage of lines containing pattern (0-100)
}

// GenerateTestFile creates a test log file based on config
func GenerateTestFile(tb testing.TB, config TestDataConfig) string {
	tb.Helper()

	// Create temp file with .tmp suffix
	tmpFile, err := os.CreateTemp("", "dtail_bench_*.log.tmp")
	if err != nil {
		tb.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	filename := tmpFile.Name()

	// Apply compression if needed
	var finalFilename string
	switch config.Compression {
	case GzipCompression:
		finalFilename = filename + ".gz"
		if err := generateCompressedFile(filename, finalFilename, config, gzipWriter); err != nil {
			tb.Fatalf("Failed to generate gzip file: %v", err)
		}
		os.Remove(filename)
		return finalFilename
	case ZstdCompression:
		finalFilename = filename + ".zst"
		if err := generateCompressedFile(filename, finalFilename, config, zstdWriter); err != nil {
			tb.Fatalf("Failed to generate zstd file: %v", err)
		}
		os.Remove(filename)
		return finalFilename
	default:
		if err := generateUncompressedFile(filename, config); err != nil {
			tb.Fatalf("Failed to generate file: %v", err)
		}
		return filename
	}
}

// generateUncompressedFile creates an uncompressed log file
func generateUncompressedFile(filename string, config TestDataConfig) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	return writeLogLines(writer, config)
}

// compressionWriter is a function that creates a compression writer
type compressionWriter func(io.Writer) (io.WriteCloser, error)

// gzipWriter creates a gzip writer
func gzipWriter(w io.Writer) (io.WriteCloser, error) {
	return gzip.NewWriter(w), nil
}

// zstdWriter creates a zstd writer
func zstdWriter(w io.Writer) (io.WriteCloser, error) {
	return zstd.NewWriterLevel(w, zstd.DefaultCompression), nil
}

// generateCompressedFile creates a compressed log file
func generateCompressedFile(tmpFile, finalFile string, config TestDataConfig, createWriter compressionWriter) error {
	// First generate uncompressed
	if err := generateUncompressedFile(tmpFile, config); err != nil {
		return err
	}

	// Read and compress
	input, err := os.Open(tmpFile)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.Create(finalFile)
	if err != nil {
		return err
	}
	defer output.Close()

	compressor, err := createWriter(output)
	if err != nil {
		return err
	}
	defer compressor.Close()

	_, err = io.Copy(compressor, input)
	return err
}

// writeLogLines generates log content based on config
func writeLogLines(w io.Writer, config TestDataConfig) error {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	// Calculate approximate lines needed
	avgLineSize := 150 // bytes
	totalLines := int(config.Size) / avgLineSize
	
	// Pre-generate some template lines for variation
	templateLines := generateTemplateLines(config.Format, config.LineVariation, config.Pattern, config.PatternRate, rng)
	
	bytesWritten := 0
	for i := 0; i < totalLines && bytesWritten < int(config.Size); i++ {
		// Pick a random template line
		line := templateLines[rng.Intn(len(templateLines))]
		
		// Write with current timestamp
		timestampedLine := strings.Replace(line, "{TIMESTAMP}", generateTimestamp(i), 1)
		timestampedLine = strings.Replace(timestampedLine, "{COUNTER}", fmt.Sprintf("%d", i), 1)
		
		n, err := fmt.Fprintln(w, timestampedLine)
		if err != nil {
			return err
		}
		bytesWritten += n
	}
	
	return nil
}

// generateTemplateLines creates a set of template log lines
func generateTemplateLines(format LogFormat, variation int, pattern string, patternRate int, rng *rand.Rand) []string {
	numTemplates := max(10, variation) // At least 10 templates
	templates := make([]string, 0, numTemplates)
	
	for i := 0; i < numTemplates; i++ {
		includePattern := pattern != "" && rng.Intn(100) < patternRate
		
		switch format {
		case SimpleLogFormat:
			templates = append(templates, generateSimpleLogLine(i, includePattern, pattern, rng))
		case MapReduceLogFormat:
			templates = append(templates, generateMapReduceLogLine(i, includePattern, pattern, rng))
		case MixedLogFormat:
			if rng.Intn(2) == 0 {
				templates = append(templates, generateSimpleLogLine(i, includePattern, pattern, rng))
			} else {
				templates = append(templates, generateMapReduceLogLine(i, includePattern, pattern, rng))
			}
		}
	}
	
	return templates
}

// generateSimpleLogLine creates a simple log line template
func generateSimpleLogLine(id int, includePattern bool, pattern string, rng *rand.Rand) string {
	levels := []string{"INFO", "WARN", "ERROR", "DEBUG"}
	level := levels[rng.Intn(len(levels))]
	
	message := fmt.Sprintf("Processing request %d", id)
	if includePattern && pattern != "" {
		message = fmt.Sprintf("%s %s", message, pattern)
	}
	
	// Format: LEVEL|TIMESTAMP|THREAD|FILE:LINE|MESSAGE
	return fmt.Sprintf("%s|{TIMESTAMP}|thread-%d|app.go:%d|%s", 
		level, rng.Intn(10)+1, rng.Intn(1000)+1, message)
}

// generateMapReduceLogLine creates a MapReduce format log line template
func generateMapReduceLogLine(id int, includePattern bool, pattern string, rng *rand.Rand) string {
	goroutines := rng.Intn(50) + 10
	connections := rng.Intn(100)
	lifetime := rng.Intn(1000) + 100
	
	message := "MAPREDUCE:STATS"
	if includePattern && pattern != "" {
		message = fmt.Sprintf("%s|%s", message, pattern)
	}
	
	// Format matching the integration test data
	return fmt.Sprintf("INFO|{TIMESTAMP}|1|stats.go:56|8|%d|7|0.%02d|471h%dm%ds|%s|currentConnections=%d|lifetimeConnections=%d",
		goroutines, rng.Intn(100), rng.Intn(60), rng.Intn(60), message, connections, lifetime)
}

// generateTimestamp creates a timestamp for log lines
func generateTimestamp(lineNum int) string {
	// Format: MMDD-HHMMSS
	baseTime := time.Date(2024, 10, 2, 7, 10, 0, 0, time.UTC)
	offsetSeconds := lineNum / 10 // Advance time every 10 lines
	t := baseTime.Add(time.Duration(offsetSeconds) * time.Second)
	return t.Format("0102-150405")
}

// CleanupBenchmarkFiles removes all benchmark temporary files
func CleanupBenchmarkFiles(pattern string) error {
	if pattern == "" {
		pattern = "dtail_bench_*.tmp*"
	}
	
	tempDir := os.TempDir()
	matches, err := filepath.Glob(filepath.Join(tempDir, pattern))
	if err != nil {
		return err
	}
	
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	
	return nil
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}