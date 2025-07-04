package pgo

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mimecast/dtail/internal/tools/common"
)

// Config holds PGO configuration
type Config struct {
	Command        string   // Command to build with PGO (dtail, dcat, etc.)
	ProfileDir     string   // Directory containing profile data
	OutputDir      string   // Directory for PGO-optimized binaries
	TestDataSize   int      // Size of test data for profile generation
	TestIterations int      // Number of iterations for profile generation
	Verbose        bool     // Verbose output
	Commands       []string // Specific commands to optimize (empty = all)
	ProfileOnly    bool     // Only generate profiles, don't build optimized binaries
}

// Run executes the PGO workflow
func Run() error {
	var cfg Config

	// Define flags
	flag.StringVar(&cfg.ProfileDir, "profiledir", "pgo-profiles", "Directory for profile data")
	flag.StringVar(&cfg.OutputDir, "outdir", "pgo-build", "Directory for PGO-optimized binaries")
	flag.IntVar(&cfg.TestDataSize, "datasize", 1000000, "Lines of test data for profile generation")
	flag.IntVar(&cfg.TestIterations, "iterations", 3, "Number of profile generation iterations")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Verbose output")
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose output (short)")
	flag.BoolVar(&cfg.ProfileOnly, "profileonly", false, "Only generate profiles, don't build optimized binaries")
	
	// Custom usage
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: dtail-tools pgo [options] [commands...]\n\n")
		fmt.Fprintf(os.Stderr, "Profile-Guided Optimization (PGO) for DTail commands\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nCommands:\n")
		fmt.Fprintf(os.Stderr, "  If no commands specified, all dtail commands will be optimized\n")
		fmt.Fprintf(os.Stderr, "  Available: dtail, dcat, dgrep, dmap, dserver\n\n")
		fmt.Fprintf(os.Stderr, "Example:\n")
		fmt.Fprintf(os.Stderr, "  dtail-tools pgo                    # Optimize all commands\n")
		fmt.Fprintf(os.Stderr, "  dtail-tools pgo dcat dgrep         # Optimize specific commands\n")
		fmt.Fprintf(os.Stderr, "  dtail-tools pgo -v -iterations 5   # Verbose with 5 iterations\n")
	}

	flag.Parse()

	// Get commands from remaining args
	cfg.Commands = flag.Args()
	if len(cfg.Commands) == 0 {
		// Default to all main commands
		cfg.Commands = []string{"dtail", "dcat", "dgrep", "dmap", "dserver"}
	}

	return runPGO(&cfg)
}

func runPGO(cfg *Config) error {
	// Create directories
	if err := os.MkdirAll(cfg.ProfileDir, 0755); err != nil {
		return fmt.Errorf("creating profile directory: %w", err)
	}
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	fmt.Println("DTail Profile-Guided Optimization")
	fmt.Println("=================================")
	fmt.Printf("Commands: %s\n", strings.Join(cfg.Commands, ", "))
	fmt.Printf("Profile directory: %s\n", cfg.ProfileDir)
	fmt.Printf("Output directory: %s\n", cfg.OutputDir)
	fmt.Printf("Test data size: %d lines\n", cfg.TestDataSize)
	fmt.Printf("Iterations: %d\n\n", cfg.TestIterations)

	// Step 1: Build baseline binaries
	fmt.Println("Step 1: Building baseline binaries...")
	if err := buildBaseline(cfg); err != nil {
		return fmt.Errorf("building baseline: %w", err)
	}

	// Step 2: Generate profiles
	fmt.Println("\nStep 2: Generating profiles...")
	if err := generateProfiles(cfg); err != nil {
		return fmt.Errorf("generating profiles: %w", err)
	}

	// If profile-only mode, stop here
	if cfg.ProfileOnly {
		fmt.Println("\nProfile generation complete!")
		fmt.Printf("Profiles saved in: %s\n", cfg.ProfileDir)
		return nil
	}

	// Step 3: Build PGO-optimized binaries
	fmt.Println("\nStep 3: Building PGO-optimized binaries...")
	if err := buildWithPGO(cfg); err != nil {
		return fmt.Errorf("building with PGO: %w", err)
	}

	// Step 4: Compare performance
	fmt.Println("\nStep 4: Comparing performance...")
	if err := comparePerformance(cfg); err != nil {
		return fmt.Errorf("comparing performance: %w", err)
	}

	fmt.Println("\nPGO optimization complete!")
	fmt.Printf("Optimized binaries are in: %s\n", cfg.OutputDir)
	
	return nil
}

func buildBaseline(cfg *Config) error {
	for _, cmd := range cfg.Commands {
		if cfg.Verbose {
			fmt.Printf("Building %s...\n", cmd)
		}
		
		// Build command
		buildCmd := exec.Command("go", "build",
			"-o", filepath.Join(cfg.OutputDir, cmd+"-baseline"),
			fmt.Sprintf("./cmd/%s", cmd))
		
		if cfg.Verbose {
			buildCmd.Stdout = os.Stdout
			buildCmd.Stderr = os.Stderr
		}
		
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("building %s: %w", cmd, err)
		}
	}
	
	return nil
}

func generateProfiles(cfg *Config) error {
	// Generate test data
	testFiles, err := generateTestData(cfg)
	if err != nil {
		return fmt.Errorf("generating test data: %w", err)
	}
	defer cleanupTestData(testFiles)

	// Run each command to generate profiles
	for _, cmd := range cfg.Commands {
		fmt.Printf("\nGenerating profile for %s...\n", cmd)
		
		profilePath := filepath.Join(cfg.ProfileDir, fmt.Sprintf("%s.pprof", cmd))
		
		// Run iterations to collect profile data
		if err := runProfileWorkload(cfg, cmd, testFiles, profilePath); err != nil {
			return fmt.Errorf("running workload for %s: %w", cmd, err)
		}
	}
	
	return nil
}

func runProfileWorkload(cfg *Config, command string, testFiles map[string]string, profilePath string) error {
	// Use the baseline binary that was already built
	binary := filepath.Join(cfg.OutputDir, command+"-baseline")
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("baseline binary not found: %s", binary)
	}

	// Merge profiles from multiple runs
	var profiles []string
	
	for i := 0; i < cfg.TestIterations; i++ {
		if cfg.Verbose {
			fmt.Printf("  Iteration %d/%d...\n", i+1, cfg.TestIterations)
		}
		
		iterProfile := fmt.Sprintf("%s.%d.pprof", profilePath, i)
		if err := runSingleWorkload(cfg, command, binary, testFiles, iterProfile); err != nil {
			return fmt.Errorf("iteration %d: %w", i+1, err)
		}
		profiles = append(profiles, iterProfile)
	}

	// Merge profiles
	if err := mergeProfiles(profiles, profilePath); err != nil {
		return fmt.Errorf("merging profiles: %w", err)
	}

	// Clean up iteration profiles
	for _, p := range profiles {
		os.Remove(p)
	}
	
	return nil
}

func runSingleWorkload(cfg *Config, command, binary string, testFiles map[string]string, profilePath string) error {
	var cmd *exec.Cmd
	
	// Use a unique profile directory for this iteration
	iterProfileDir := filepath.Join(cfg.ProfileDir, fmt.Sprintf("iter_%s_%d", command, time.Now().UnixNano()))
	if err := os.MkdirAll(iterProfileDir, 0755); err != nil {
		return fmt.Errorf("creating iteration profile dir: %w", err)
	}
	defer os.RemoveAll(iterProfileDir)
	
	switch command {
	case "dtail":
		// For dtail, we need to simulate a growing log file
		// First, create an empty file
		growingLog := testFiles["growing_log"]
		if err := os.WriteFile(growingLog, []byte{}, 0644); err != nil {
			return fmt.Errorf("creating growing log: %w", err)
		}
		
		// Start a background process to write to the file with various log levels
		writerCmd := exec.Command("bash", "-c", fmt.Sprintf(
			"for i in {1..200}; do level=$((i %% 4)); case $level in 0) lvl=INFO;; 1) lvl=WARN;; 2) lvl=ERROR;; 3) lvl=DEBUG;; esac; echo \"[2025-07-04 15:00:00] $lvl - Test log line number $i with some additional text to process\" >> %s; sleep 0.015; done",
			growingLog))
		if err := writerCmd.Start(); err != nil {
			return fmt.Errorf("starting log writer: %w", err)
		}
		defer writerCmd.Process.Kill()
		
		// Run dtail to follow the growing file with regex filtering
		cmd = exec.Command(binary,
			"-cfg", "none",
			"-plain",
			"-profile",
			"-profiledir", iterProfileDir,
			"-regex", "ERROR|WARN",  // Filter for errors and warnings
			"-shutdownAfter", "3",  // Exit after 3 seconds
			growingLog)
		
	case "dcat":
		cmd = exec.Command(binary,
			"-cfg", "none",
			"-plain",
			"-profile",
			"-profiledir", iterProfileDir,
			testFiles["log"])
		
	case "dgrep":
		cmd = exec.Command(binary,
			"-cfg", "none",
			"-plain",
			"-profile",
			"-profiledir", iterProfileDir,
			"-regex", "ERROR|WARN",
			testFiles["log"])
		
	case "dmap":
		cmd = exec.Command(binary,
			"-cfg", "none",
			"-plain",
			"-profile",
			"-profiledir", iterProfileDir,
			"-files", testFiles["csv"],
			"-query", "select status, count(*) group by status")
		
	case "dserver":
		// For dserver, we'll simulate some client connections
		return runDServerWorkload(cfg, binary, testFiles, profilePath)
		
	default:
		return fmt.Errorf("unknown command: %s", command)
	}
	
	// Capture stderr for debugging
	if cfg.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	
	// Run command
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %s: %w", command, err)
	}
	
	// Find the generated CPU profile
	generatedProfile := filepath.Join(iterProfileDir, fmt.Sprintf("%s_cpu_*.prof", command))
	matches, err := filepath.Glob(generatedProfile)
	if err != nil || len(matches) == 0 {
		return fmt.Errorf("no CPU profile generated (looked for %s)", generatedProfile)
	}
	
	// Use the first match
	return copyFile(matches[0], profilePath)
}

// copyFile copies src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	
	_, err = io.Copy(dstFile, srcFile)
	return err
}

func runDServerWorkload(cfg *Config, binary string, testFiles map[string]string, profilePath string) error {
	// Start dserver with pprof endpoint
	serverCmd := exec.Command(binary,
		"-cfg", "none",
		"-pprof", "localhost:16060", // pprof endpoint
		"-port", "12222") // Use non-standard port
	
	if cfg.Verbose {
		serverCmd.Stdout = os.Stdout
		serverCmd.Stderr = os.Stderr
	}
	
	if err := serverCmd.Start(); err != nil {
		return fmt.Errorf("starting dserver: %w", err)
	}
	
	// Give server time to start
	time.Sleep(2 * time.Second)
	
	// Check if server is actually running
	if serverCmd.Process == nil {
		return fmt.Errorf("dserver process not started")
	}
	
	// Run multiple client commands against it to generate load
	clients := []struct {
		cmd  string
		args []string
	}{
		{"dcat", []string{"-cfg", "none", "-server", "localhost:12222", testFiles["log"]}},
		{"dgrep", []string{"-cfg", "none", "-server", "localhost:12222", "-regex", "ERROR|WARN", testFiles["log"]}},
		{"dgrep", []string{"-cfg", "none", "-server", "localhost:12222", "-regex", "INFO.*action", testFiles["log"]}},
		{"dmap", []string{"-cfg", "none", "-server", "localhost:12222", "-files", testFiles["csv"], "-query", "select status, count(*) group by status"}},
		{"dmap", []string{"-cfg", "none", "-server", "localhost:12222", "-files", testFiles["csv"], "-query", "select department, avg(salary) group by department"}},
	}
	
	// Run clients concurrently to generate more server load
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ { // Run each client twice
		for _, client := range clients {
			wg.Add(1)
			go func(c struct{ cmd string; args []string }) {
				defer wg.Done()
				cmd := exec.Command(filepath.Join(cfg.OutputDir, c.cmd+"-baseline"), c.args...)
				cmd.Stdout = io.Discard
				cmd.Stderr = io.Discard
				cmd.Run() // Ignore errors
			}(client)
		}
	}
	
	// Start CPU profiling in a goroutine
	profileDone := make(chan error, 1)
	go func() {
		// Give a moment for workload to start
		time.Sleep(500 * time.Millisecond)
		
		// Capture CPU profile from pprof endpoint (this blocks for 5 seconds)
		resp, err := http.Get("http://localhost:16060/debug/pprof/profile?seconds=5")
		if err != nil {
			profileDone <- fmt.Errorf("capturing profile: %w", err)
			return
		}
		defer resp.Body.Close()
		
		// Write profile to file
		outFile, err := os.Create(profilePath)
		if err != nil {
			profileDone <- fmt.Errorf("creating profile file: %w", err)
			return
		}
		defer outFile.Close()
		
		if _, err := io.Copy(outFile, resp.Body); err != nil {
			profileDone <- fmt.Errorf("writing profile: %w", err)
			return
		}
		
		profileDone <- nil
	}()
	
	// Run the workload while profiling
	wg.Wait()
	
	// Wait for profiling to complete
	if err := <-profileDone; err != nil {
		serverCmd.Process.Kill()
		serverCmd.Wait()
		return err
	}
	
	// Stop server gracefully
	serverCmd.Process.Signal(os.Interrupt)
	time.Sleep(500 * time.Millisecond)
	serverCmd.Process.Kill()
	serverCmd.Wait()
	
	return nil
}

func mergeProfiles(profiles []string, output string) error {
	if len(profiles) == 0 {
		return fmt.Errorf("no profiles to merge")
	}
	
	// Filter out empty profiles
	var validProfiles []string
	for _, profile := range profiles {
		info, err := os.Stat(profile)
		if err != nil {
			continue
		}
		if info.Size() > 0 {
			validProfiles = append(validProfiles, profile)
		}
	}
	
	if len(validProfiles) == 0 {
		// All profiles are empty, create an empty output file
		// This allows the workflow to continue
		fmt.Printf("Warning: All profiles for this command are empty (I/O-bound operation?)\n")
		return os.WriteFile(output, []byte{}, 0644)
	}
	
	if len(validProfiles) == 1 {
		// Just rename
		return os.Rename(validProfiles[0], output)
	}
	
	// Use go tool pprof to merge
	args := append([]string{"tool", "pprof", "-proto"}, validProfiles...)
	cmd := exec.Command("go", args...)
	
	outFile, err := os.Create(output)
	if err != nil {
		return err
	}
	defer outFile.Close()
	
	cmd.Stdout = outFile
	
	return cmd.Run()
}

func buildWithPGO(cfg *Config) error {
	for _, cmd := range cfg.Commands {
		profilePath := filepath.Join(cfg.ProfileDir, fmt.Sprintf("%s.pprof", cmd))
		
		// Check if profile exists and is not empty
		info, err := os.Stat(profilePath)
		if err != nil {
			fmt.Printf("Warning: No profile found for %s, skipping PGO build\n", cmd)
			continue
		}
		if info.Size() == 0 {
			fmt.Printf("Warning: Profile for %s is empty, skipping PGO build\n", cmd)
			continue
		}
		
		if cfg.Verbose {
			fmt.Printf("Building %s with PGO...\n", cmd)
		}
		
		// Build with PGO
		buildCmd := exec.Command("go", "build",
			"-pgo", profilePath,
			"-o", filepath.Join(cfg.OutputDir, cmd),
			fmt.Sprintf("./cmd/%s", cmd))
		
		if cfg.Verbose {
			buildCmd.Stdout = os.Stdout
			buildCmd.Stderr = os.Stderr
		}
		
		if err := buildCmd.Run(); err != nil {
			return fmt.Errorf("building %s with PGO: %w", cmd, err)
		}
	}
	
	return nil
}

func comparePerformance(cfg *Config) error {
	// Generate small test data for quick benchmark
	testFiles, err := generateSmallTestData()
	if err != nil {
		return err
	}
	defer cleanupTestData(testFiles)

	fmt.Println("\nPerformance Comparison:")
	fmt.Println("----------------------")
	
	for _, cmd := range cfg.Commands {
		baseline := filepath.Join(cfg.OutputDir, cmd+"-baseline")
		optimized := filepath.Join(cfg.OutputDir, cmd)
		
		// Skip if either binary doesn't exist
		if _, err := os.Stat(baseline); err != nil {
			continue
		}
		if _, err := os.Stat(optimized); err != nil {
			continue
		}
		
		fmt.Printf("\n%s:\n", cmd)
		
		// Run benchmark
		baselineTime := benchmarkCommand(baseline, cmd, testFiles)
		optimizedTime := benchmarkCommand(optimized, cmd, testFiles)
		
		if baselineTime > 0 && optimizedTime > 0 {
			improvement := (float64(baselineTime) - float64(optimizedTime)) / float64(baselineTime) * 100
			fmt.Printf("  Baseline:  %.3fs\n", baselineTime.Seconds())
			fmt.Printf("  Optimized: %.3fs\n", optimizedTime.Seconds())
			fmt.Printf("  Improvement: %.1f%%\n", improvement)
		}
	}
	
	return nil
}

func benchmarkCommand(binary, command string, testFiles map[string]string) time.Duration {
	var cmd *exec.Cmd
	
	switch command {
	case "dcat":
		cmd = exec.Command(binary, "-cfg", "none", "-plain", testFiles["log"])
	case "dgrep":
		cmd = exec.Command(binary, "-cfg", "none", "-plain", "-regex", "ERROR", testFiles["log"])
	case "dmap":
		cmd = exec.Command(binary, "-cfg", "none", "-plain", "-files", testFiles["csv"],
			"-query", "select count(*)")
	default:
		return 0
	}
	
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	
	start := time.Now()
	cmd.Run()
	return time.Since(start)
}

func generateTestData(cfg *Config) (map[string]string, error) {
	files := make(map[string]string)
	
	// Generate log file
	logFile := filepath.Join(cfg.ProfileDir, "test.log")
	if err := common.GenerateLogFile(logFile, cfg.TestDataSize); err != nil {
		return nil, err
	}
	files["log"] = logFile
	
	// Generate CSV file
	csvFile := filepath.Join(cfg.ProfileDir, "test.csv")
	if err := common.GenerateCSVFile(csvFile, cfg.TestDataSize/10); err != nil {
		return nil, err
	}
	files["csv"] = csvFile
	
	// For dtail, also create a growing log file
	growingLogFile := filepath.Join(cfg.ProfileDir, "growing.log")
	files["growing_log"] = growingLogFile
	
	return files, nil
}

func generateSmallTestData() (map[string]string, error) {
	files := make(map[string]string)
	
	// Generate small files for quick benchmarks
	logFile := "/tmp/pgo_bench.log"
	if err := common.GenerateLogFile(logFile, 10000); err != nil {
		return nil, err
	}
	files["log"] = logFile
	
	csvFile := "/tmp/pgo_bench.csv"
	if err := common.GenerateCSVFile(csvFile, 1000); err != nil {
		return nil, err
	}
	files["csv"] = csvFile
	
	return files, nil
}

func cleanupTestData(files map[string]string) {
	for _, f := range files {
		os.Remove(f)
	}
}