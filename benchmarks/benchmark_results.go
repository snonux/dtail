package benchmarks

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ResultLogger handles benchmark result logging
type ResultLogger struct {
	results []BenchmarkResult
}

// NewResultLogger creates a new result logger
func NewResultLogger() *ResultLogger {
	return &ResultLogger{
		results: make([]BenchmarkResult, 0),
	}
}

// AddResult adds a benchmark result to the logger
func (rl *ResultLogger) AddResult(result BenchmarkResult) {
	result.GitCommit = GetGitCommit()
	result.GoVersion = GetGoVersion()
	rl.results = append(rl.results, result)
}

// WriteJSON writes results to a JSON file
func (rl *ResultLogger) WriteJSON(filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(rl.results)
}

// WriteCSV writes results to a CSV file
func (rl *ResultLogger) WriteCSV(filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	
	writer := csv.NewWriter(file)
	defer writer.Flush()
	
	// Write header
	header := []string{
		"Timestamp",
		"Tool",
		"Operation",
		"FileSize",
		"Duration",
		"Throughput_MB_sec",
		"Lines_per_sec",
		"Memory_MB",
		"ExitCode",
		"GitCommit",
		"GoVersion",
		"Error",
	}
	if err := writer.Write(header); err != nil {
		return err
	}
	
	// Write data
	for _, result := range rl.results {
		record := []string{
			result.Timestamp.Format(time.RFC3339),
			result.Tool,
			result.Operation,
			fmt.Sprintf("%d", result.FileSize),
			result.Duration.String(),
			fmt.Sprintf("%.2f", result.Throughput),
			fmt.Sprintf("%.2f", result.LinesPerSec),
			fmt.Sprintf("%.2f", float64(result.MemoryUsage)/(1024*1024)),
			fmt.Sprintf("%d", result.ExitCode),
			result.GitCommit,
			result.GoVersion,
			fmt.Sprintf("%v", result.Error),
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	
	return nil
}

// WriteMarkdown writes a human-readable markdown report
func (rl *ResultLogger) WriteMarkdown(filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	
	fmt.Fprintf(file, "# DTail Benchmark Results\n\n")
	fmt.Fprintf(file, "**Date**: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(file, "**Git Commit**: %s\n", GetGitCommit())
	fmt.Fprintf(file, "**Go Version**: %s\n\n", GetGoVersion())
	
	// Group results by tool
	byTool := make(map[string][]BenchmarkResult)
	for _, result := range rl.results {
		byTool[result.Tool] = append(byTool[result.Tool], result)
	}
	
	// Sort tools for consistent output
	var tools []string
	for tool := range byTool {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	
	// Write results for each tool
	for _, tool := range tools {
		fmt.Fprintf(file, "## %s\n\n", strings.ToUpper(tool))
		
		// Create table
		fmt.Fprintln(file, "| Operation | File Size | Duration | Throughput (MB/s) | Lines/sec |")
		fmt.Fprintln(file, "|-----------|-----------|----------|-------------------|-----------|")
		
		// Sort results by operation name
		results := byTool[tool]
		sort.Slice(results, func(i, j int) bool {
			return results[i].Operation < results[j].Operation
		})
		
		for _, result := range results {
			fmt.Fprintf(file, "| %s | %s | %v | %.2f | %.0f |\n",
				result.Operation,
				formatFileSize(result.FileSize),
				result.Duration.Round(time.Millisecond),
				result.Throughput,
				result.LinesPerSec,
			)
		}
		
		fmt.Fprintln(file, "")
	}
	
	return nil
}

// formatFileSize formats bytes into human-readable size
func formatFileSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ComparisonReport represents a performance comparison between two runs
type ComparisonReport struct {
	Improvements []ComparisonEntry
	Regressions  []ComparisonEntry
	Unchanged    []ComparisonEntry
}

// ComparisonEntry represents a single comparison result
type ComparisonEntry struct {
	Tool         string
	Operation    string
	BaselineDur  time.Duration
	CurrentDur   time.Duration
	ChangePercent float64
}

// CompareResults compares baseline results with current results
func CompareResults(baseline, current []BenchmarkResult) ComparisonReport {
	// Create maps for easy lookup
	baselineMap := make(map[string]BenchmarkResult)
	for _, result := range baseline {
		key := fmt.Sprintf("%s:%s", result.Tool, result.Operation)
		baselineMap[key] = result
	}
	
	currentMap := make(map[string]BenchmarkResult)
	for _, result := range current {
		key := fmt.Sprintf("%s:%s", result.Tool, result.Operation)
		currentMap[key] = result
	}
	
	report := ComparisonReport{
		Improvements: []ComparisonEntry{},
		Regressions:  []ComparisonEntry{},
		Unchanged:    []ComparisonEntry{},
	}
	
	// Compare each current result with baseline
	for key, currentResult := range currentMap {
		baselineResult, exists := baselineMap[key]
		if !exists {
			continue // Skip new benchmarks
		}
		
		// Calculate percentage change
		changePercent := ((float64(currentResult.Duration) - float64(baselineResult.Duration)) / float64(baselineResult.Duration)) * 100
		
		entry := ComparisonEntry{
			Tool:          currentResult.Tool,
			Operation:     currentResult.Operation,
			BaselineDur:   baselineResult.Duration,
			CurrentDur:    currentResult.Duration,
			ChangePercent: changePercent,
		}
		
		// Categorize based on change threshold (10%)
		switch {
		case changePercent < -10:
			report.Improvements = append(report.Improvements, entry)
		case changePercent > 10:
			report.Regressions = append(report.Regressions, entry)
		default:
			report.Unchanged = append(report.Unchanged, entry)
		}
	}
	
	// Sort by change percentage
	sort.Slice(report.Improvements, func(i, j int) bool {
		return report.Improvements[i].ChangePercent < report.Improvements[j].ChangePercent
	})
	sort.Slice(report.Regressions, func(i, j int) bool {
		return report.Regressions[i].ChangePercent > report.Regressions[j].ChangePercent
	})
	
	return report
}

// WriteComparisonReport writes a comparison report to a file
func WriteComparisonReport(report ComparisonReport, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	
	fmt.Fprintln(file, "# Performance Comparison Report")
	fmt.Fprintln(file, "")
	
	// Write regressions
	if len(report.Regressions) > 0 {
		fmt.Fprintln(file, "## ⚠️ Performance Regressions")
		fmt.Fprintln(file, "")
		fmt.Fprintln(file, "| Tool | Operation | Baseline | Current | Change |")
		fmt.Fprintln(file, "|------|-----------|----------|---------|--------|")
		
		for _, entry := range report.Regressions {
			fmt.Fprintf(file, "| %s | %s | %v | %v | +%.1f%% |\n",
				entry.Tool,
				entry.Operation,
				entry.BaselineDur.Round(time.Millisecond),
				entry.CurrentDur.Round(time.Millisecond),
				entry.ChangePercent,
			)
		}
		fmt.Fprintln(file, "")
	}
	
	// Write improvements
	if len(report.Improvements) > 0 {
		fmt.Fprintln(file, "## ✅ Performance Improvements")
		fmt.Fprintln(file, "")
		fmt.Fprintln(file, "| Tool | Operation | Baseline | Current | Change |")
		fmt.Fprintln(file, "|------|-----------|----------|---------|--------|")
		
		for _, entry := range report.Improvements {
			fmt.Fprintf(file, "| %s | %s | %v | %v | %.1f%% |\n",
				entry.Tool,
				entry.Operation,
				entry.BaselineDur.Round(time.Millisecond),
				entry.CurrentDur.Round(time.Millisecond),
				entry.ChangePercent,
			)
		}
		fmt.Fprintln(file, "")
	}
	
	// Summary
	fmt.Fprintln(file, "## Summary")
	fmt.Fprintln(file, "")
	fmt.Fprintf(file, "- Regressions: %d\n", len(report.Regressions))
	fmt.Fprintf(file, "- Improvements: %d\n", len(report.Improvements))
	fmt.Fprintf(file, "- Unchanged: %d\n", len(report.Unchanged))
	
	return nil
}

// SaveResults saves benchmark results in multiple formats
func SaveResults(results []BenchmarkResult) error {
	logger := NewResultLogger()
	for _, result := range results {
		logger.AddResult(result)
	}
	
	timestamp := time.Now().Format("20060102_150405")
	baseDir := "benchmark_results"
	
	// Create results directory
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return err
	}
	
	// Save in different formats
	jsonFile := filepath.Join(baseDir, fmt.Sprintf("results_%s.json", timestamp))
	if err := logger.WriteJSON(jsonFile); err != nil {
		return err
	}
	
	csvFile := filepath.Join(baseDir, fmt.Sprintf("results_%s.csv", timestamp))
	if err := logger.WriteCSV(csvFile); err != nil {
		return err
	}
	
	mdFile := filepath.Join(baseDir, fmt.Sprintf("results_%s.md", timestamp))
	if err := logger.WriteMarkdown(mdFile); err != nil {
		return err
	}
	
	// Also save as latest for easy access
	latestJSON := filepath.Join(baseDir, "latest.json")
	if err := logger.WriteJSON(latestJSON); err != nil {
		return err
	}
	
	return nil
}