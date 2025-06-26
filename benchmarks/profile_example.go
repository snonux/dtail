package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Example of using the profiling framework to find performance bottlenecks
func main() {
	fmt.Println("DTail Profiling Example")
	fmt.Println("======================")
	fmt.Println()

	// Create test data
	testFile := createTestData()
	defer os.Remove(testFile)

	// Profile dcat
	fmt.Println("1. Profiling dcat...")
	profileDCat(testFile)

	// Profile dgrep
	fmt.Println("\n2. Profiling dgrep...")
	profileDGrep(testFile)

	// Profile dmap
	csvFile := createCSVData()
	defer os.Remove(csvFile)
	fmt.Println("\n3. Profiling dmap...")
	profileDMap(csvFile)

	// Analyze results
	fmt.Println("\n4. Analyzing profiles...")
	analyzeProfiles()
}

func createTestData() string {
	filename := "test_data.log"
	f, err := os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Generate 100MB of log data
	for i := 0; i < 1000000; i++ {
		timestamp := time.Now().Format("2006-01-02 15:04:05.000")
		level := []string{"INFO", "WARN", "ERROR", "DEBUG"}[i%4]
		fmt.Fprintf(f, "[%s] %s - Processing request %d from user%d\n", 
			timestamp, level, i, i%1000)
	}

	return filename
}

func createCSVData() string {
	filename := "test_data.csv"
	f, err := os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Header
	fmt.Fprintln(f, "timestamp,user,action,duration,status")

	// Generate data
	for i := 0; i < 100000; i++ {
		timestamp := time.Now().Add(time.Duration(i) * time.Second).Format("2006-01-02 15:04:05")
		user := fmt.Sprintf("user%d", i%100)
		action := []string{"login", "query", "update", "logout"}[i%4]
		duration := 100 + i%900
		status := []string{"success", "failure"}[i%2]
		
		fmt.Fprintf(f, "%s,%s,%s,%d,%s\n", timestamp, user, action, duration, status)
	}

	return filename
}

func profileDCat(testFile string) {
	// Run dcat with profiling
	cmd := exec.Command("../dcat", 
		"-profile",
		"-profiledir", "profiles",
		"-plain",
		"-cfg", "none",
		testFile)

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		fmt.Printf("Output: %s\n", output)
		return
	}

	fmt.Printf("  Completed in %v\n", duration)
	
	// Find generated profiles
	profiles, _ := filepath.Glob("profiles/dcat_*.prof")
	for _, p := range profiles {
		info, _ := os.Stat(p)
		fmt.Printf("  Generated: %s (%d KB)\n", filepath.Base(p), info.Size()/1024)
	}
}

func profileDGrep(testFile string) {
	// Run dgrep with profiling
	cmd := exec.Command("../dgrep",
		"-profile",
		"-profiledir", "profiles",
		"-plain",
		"-cfg", "none",
		"-regex", "ERROR|WARN",
		"-before", "2",
		"-after", "2",
		testFile)

	start := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	if err != nil {
		fmt.Printf("Error: %v\n", err)
		fmt.Printf("Output: %s\n", output)
		return
	}

	fmt.Printf("  Completed in %v\n", duration)
	
	// Count matches
	matches := strings.Count(string(output), "ERROR") + strings.Count(string(output), "WARN")
	fmt.Printf("  Found %d matches\n", matches)
}

func profileDMap(csvFile string) {
	// Get absolute path for the CSV file
	absPath, err := filepath.Abs(csvFile)
	if err != nil {
		fmt.Printf("Error getting absolute path: %v\n", err)
		return
	}
	
	// Run dmap with profiling
	queries := []string{
		fmt.Sprintf("select count(*) from %s", absPath),
		fmt.Sprintf("select user, count(*) from %s group by user", absPath),
		fmt.Sprintf("select action, avg(duration), max(duration) from %s group by action", absPath),
	}

	for i, query := range queries {
		fmt.Printf("  Query %d: %s\n", i+1, truncateQuery(query))
		
		cmd := exec.Command("../dmap",
			"-profile",
			"-profiledir", "profiles",
			"-plain",
			"-cfg", "none",
			"-query", query)

		start := time.Now()
		_, err := cmd.CombinedOutput()
		duration := time.Since(start)

		if err != nil {
			fmt.Printf("    Error: %v\n", err)
			continue
		}

		fmt.Printf("    Completed in %v\n", duration)
	}
}

func truncateQuery(query string) string {
	if len(query) > 50 {
		return query[:47] + "..."
	}
	return query
}

func analyzeProfiles() {
	// Find latest CPU profiles
	cpuProfiles, _ := filepath.Glob("profiles/*_cpu_*.prof")
	if len(cpuProfiles) == 0 {
		fmt.Println("No CPU profiles found")
		return
	}

	// Analyze each tool's CPU profile
	tools := []string{"dcat", "dgrep", "dmap"}
	for _, tool := range tools {
		var latestProfile string
		var latestTime time.Time

		// Find latest profile for this tool
		for _, profile := range cpuProfiles {
			if strings.Contains(profile, tool+"_cpu_") {
				info, err := os.Stat(profile)
				if err == nil && info.ModTime().After(latestTime) {
					latestProfile = profile
					latestTime = info.ModTime()
				}
			}
		}

		if latestProfile == "" {
			continue
		}

		fmt.Printf("\nAnalyzing %s CPU profile:\n", tool)
		
		// Run profile.sh
		cmd := exec.Command("../profiling/profile.sh",
			"-top", "5",
			latestProfile)
		
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("  Error analyzing: %v\n", err)
			continue
		}

		// Extract and display key information
		lines := strings.Split(string(output), "\n")
		inTable := false
		for _, line := range lines {
			if strings.Contains(line, "Function") && strings.Contains(line, "Flat") {
				inTable = true
			}
			if inTable && (strings.Contains(line, "%") || strings.Contains(line, "---")) {
				fmt.Printf("  %s\n", line)
			}
			if inTable && line == "" {
				break
			}
		}
		
		// Suggest optimizations based on findings
		suggestOptimizations(tool, string(output))
	}
}

func suggestOptimizations(tool string, analysis string) {
	fmt.Printf("\n  Optimization suggestions for %s:\n", tool)
	
	// Common patterns to look for
	suggestions := []struct {
		pattern string
		suggestion string
	}{
		{"regexp.Compile", "  - Pre-compile regex patterns instead of compiling in loops"},
		{"strings.Join", "  - Use strings.Builder for string concatenation"},
		{"runtime.mallocgc", "  - High allocation rate; consider object pooling"},
		{"syscall", "  - I/O bottleneck; consider buffering or async I/O"},
		{"runtime.gcBgMarkWorker", "  - High GC pressure; reduce allocations"},
	}
	
	foundAny := false
	for _, s := range suggestions {
		if strings.Contains(analysis, s.pattern) {
			fmt.Println(s.suggestion)
			foundAny = true
		}
	}
	
	if !foundAny {
		fmt.Println("  - Profile looks good; no obvious bottlenecks found")
	}
}

// Helper function to demonstrate how to use profiling in tests
func ExampleBenchmarkWithProfiling() {
	// This would typically be in a _test.go file
	fmt.Println(`
Example benchmark with profiling:

func BenchmarkDCatLargeFile(b *testing.B) {
    // Enable profiling for this specific benchmark
    if *cpuprofile != "" {
        f, _ := os.Create(*cpuprofile)
        pprof.StartCPUProfile(f)
        defer pprof.StopCPUProfile()
    }
    
    // Generate test file
    testFile := generateLargeFile(b)
    defer os.Remove(testFile)
    
    b.ResetTimer()
    
    for i := 0; i < b.N; i++ {
        cmd := exec.Command("./dcat", "-plain", testFile)
        cmd.Run()
    }
    
    if *memprofile != "" {
        f, _ := os.Create(*memprofile)
        runtime.GC()
        pprof.WriteHeapProfile(f)
        f.Close()
    }
}

Run with: go test -bench=BenchmarkDCatLargeFile -cpuprofile=cpu.prof -memprofile=mem.prof
`)
}