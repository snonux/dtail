package benchmark

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/tools/common"
)

// Config holds benchmark configuration
type Config struct {
	Mode         string
	BaselineDir  string
	Tag          string
	Quick        bool
	Memory       bool
	OutputFile   string
	Verbose      bool
	Iterations   string
	BaselinePath string
}

// Run executes the benchmark command
func Run() error {
	cfg := parseFlags()

	// Create baseline directory if needed
	if err := common.EnsureDirectory(cfg.BaselineDir); err != nil {
		return fmt.Errorf("failed to create baseline directory: %w", err)
	}

	switch cfg.Mode {
	case "run":
		return runBenchmarks(cfg)
	case "baseline":
		return createBaseline(cfg)
	case "compare":
		return compareWithBaseline(cfg)
	case "list":
		return listBaselines(cfg)
	case "clean":
		return cleanBaselines(cfg)
	default:
		return fmt.Errorf("unknown benchmark mode: %s", cfg.Mode)
	}
}

func parseFlags() *Config {
	cfg := &Config{
		BaselineDir: "benchmarks/baselines",
		Iterations:  "1x",
	}

	flag.StringVar(&cfg.Mode, "mode", "run", "Benchmark mode: run, baseline, compare, list, clean")
	flag.StringVar(&cfg.BaselineDir, "dir", cfg.BaselineDir, "Baseline directory")
	flag.StringVar(&cfg.Tag, "tag", "", "Tag for baseline (e.g., 'before-optimization')")
	flag.BoolVar(&cfg.Quick, "quick", false, "Run only quick benchmarks")
	flag.BoolVar(&cfg.Memory, "memory", false, "Include memory profiling")
	flag.StringVar(&cfg.OutputFile, "output", "", "Output file for results")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Verbose output")
	flag.StringVar(&cfg.Iterations, "iterations", cfg.Iterations, "Benchmark iterations (e.g., 3x)")
	flag.StringVar(&cfg.BaselinePath, "baseline", "", "Baseline file for comparison")

	flag.Parse()

	// Handle positional arguments for compare mode
	if cfg.Mode == "compare" && cfg.BaselinePath == "" {
		args := flag.Args()
		if len(args) > 0 {
			cfg.BaselinePath = args[0]
		}
	}

	return cfg
}

func runBenchmarks(cfg *Config) error {
	common.PrintSection("Running DTail Benchmarks")

	// Build binaries
	common.PrintInfo("Building binaries...\n")
	if err := common.BuildCommands("dcat", "dgrep", "dmap", "dtail", "dserver"); err != nil {
		return fmt.Errorf("failed to build binaries: %w", err)
	}

	// Prepare benchmark command
	args := []string{"test", "-bench=."}
	if cfg.Quick {
		args = append(args, "-bench=BenchmarkQuick")
	}
	if cfg.Memory {
		args = append(args, "-benchmem")
	}
	if cfg.Iterations != "1x" {
		args = append(args, fmt.Sprintf("-benchtime=%s", cfg.Iterations))
	}
	if cfg.Verbose {
		args = append(args, "-v")
	}
	args = append(args, "./benchmarks")

	// Run benchmarks
	cmd := exec.Command("go", args...)
	
	var output []byte
	var err error
	
	if cfg.OutputFile != "" {
		// Capture output for file
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("benchmark failed: %w\n%s", err, string(output))
		}
		
		// Write to file
		if err := os.WriteFile(cfg.OutputFile, output, 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		
		// Also print to stdout
		fmt.Print(string(output))
		common.PrintSuccess("\nResults saved to: %s\n", cfg.OutputFile)
	} else {
		// Direct output to stdout
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("benchmark failed: %w", err)
		}
	}

	return nil
}

func createBaseline(cfg *Config) error {
	if cfg.Tag == "" {
		return fmt.Errorf("baseline tag is required (use -tag)")
	}

	common.PrintSection("Creating Benchmark Baseline")

	// Generate filename
	timestamp := time.Now().Format("20060102_150405")
	safeTag := strings.ReplaceAll(cfg.Tag, " ", "_")
	safeTag = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || 
		   (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, safeTag)
	
	filename := filepath.Join(cfg.BaselineDir, 
		fmt.Sprintf("baseline_%s_%s.txt", timestamp, safeTag))

	// Create baseline file with metadata
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create baseline file: %w", err)
	}
	defer file.Close()

	// Write metadata
	fmt.Fprintf(file, "Git commit: %s\n", common.GetGitCommit())
	fmt.Fprintf(file, "Date: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(file, "Tag: %s\n", cfg.Tag)
	fmt.Fprintf(file, "----------------------------------------\n")

	// Run benchmarks and capture output
	args := []string{"test", "-bench=.", "-benchmem"}
	if cfg.Quick {
		args = append(args, "-bench=BenchmarkQuick")
	}
	if cfg.Iterations != "1x" && cfg.Iterations != "" {
		args = append(args, fmt.Sprintf("-benchtime=%s", cfg.Iterations))
	}
	args = append(args, "./benchmarks")

	cmd := exec.Command("go", args...)
	cmd.Stdout = io.MultiWriter(file, os.Stdout)
	cmd.Stderr = os.Stderr

	common.PrintInfo("Running benchmarks for baseline...\n")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("benchmark failed: %w", err)
	}

	common.PrintSuccess("\nBaseline saved to: %s\n", filename)
	return nil
}

func compareWithBaseline(cfg *Config) error {
	if cfg.BaselinePath == "" {
		return fmt.Errorf("baseline file required (use -baseline or specify as argument)")
	}

	if !common.FileExists(cfg.BaselinePath) {
		return fmt.Errorf("baseline file not found: %s", cfg.BaselinePath)
	}

	common.PrintSection("Comparing with Baseline")
	fmt.Printf("Baseline: %s\n\n", cfg.BaselinePath)

	// Run current benchmarks
	currentFile := filepath.Join(cfg.BaselineDir, "current.txt")
	args := []string{"test", "-bench=.", "-benchmem"}
	
	// Check if baseline is quick mode
	baselineContent, err := os.ReadFile(cfg.BaselinePath)
	if err != nil {
		return fmt.Errorf("failed to read baseline: %w", err)
	}
	if strings.Contains(string(baselineContent), "BenchmarkQuick") {
		args = append(args, "-bench=BenchmarkQuick")
	}
	
	args = append(args, "./benchmarks")

	cmd := exec.Command("go", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("benchmark failed: %w\n%s", err, string(output))
	}

	// Save current results
	if err := os.WriteFile(currentFile, output, 0644); err != nil {
		return fmt.Errorf("failed to write current results: %w", err)
	}

	// Print current results
	fmt.Println("Current benchmark results:")
	fmt.Println(string(output))
	
	common.PrintSection("Comparison Report")

	// Try benchstat first
	if err := runBenchstat(cfg.BaselinePath, currentFile); err != nil {
		// Fall back to simple diff
		common.PrintInfo("benchstat not found, showing simple diff:\n\n")
		if err := showSimpleDiff(cfg.BaselinePath, currentFile); err != nil {
			return fmt.Errorf("failed to show diff: %w", err)
		}
	}

	// Save comparison report
	reportFile := filepath.Join(cfg.BaselineDir, 
		fmt.Sprintf("comparison_%s.txt", time.Now().Format("20060102_150405")))
	
	report := fmt.Sprintf("Comparison Report\n"+
		"Generated: %s\n"+
		"Baseline: %s\n"+
		"Current: %s\n"+
		"================================================================================\n\n",
		time.Now().Format(time.RFC3339),
		cfg.BaselinePath,
		currentFile)
	
	if err := os.WriteFile(reportFile, []byte(report), 0644); err != nil {
		common.PrintError("Failed to save comparison report: %v\n", err)
	} else {
		common.PrintInfo("\nComparison report saved to: %s\n", reportFile)
	}

	return nil
}

func listBaselines(cfg *Config) error {
	common.PrintSection("Available Baselines")

	pattern := filepath.Join(cfg.BaselineDir, "baseline_*.txt")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to list baselines: %w", err)
	}

	if len(files) == 0 {
		fmt.Printf("No baselines found in %s\n", cfg.BaselineDir)
		return nil
	}

	// Sort by modification time (newest first)
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		return fi.ModTime().After(fj.ModTime())
	})

	// Display baselines
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		// Try to extract tag from file
		tag := extractTagFromBaseline(file)
		
		fmt.Printf("  %s  %8s  %-40s %s\n",
			info.ModTime().Format("2006-01-02 15:04:05"),
			common.FormatSize(info.Size()),
			filepath.Base(file),
			tag)
	}

	fmt.Printf("\nTotal: %d baselines\n", len(files))
	fmt.Printf("\nUsage: dtail-tools benchmark -mode compare <baseline_file>\n")
	
	return nil
}

func cleanBaselines(cfg *Config) error {
	common.PrintSection("Cleaning Old Baselines")

	pattern := filepath.Join(cfg.BaselineDir, "baseline_*.txt")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("failed to list baselines: %w", err)
	}

	if len(files) <= 10 {
		fmt.Println("No old baselines to clean (keeping last 10)")
		return nil
	}

	// Sort by modification time (oldest first)
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		return fi.ModTime().Before(fj.ModTime())
	})

	// Remove old files
	toRemove := files[:len(files)-10]
	for _, file := range toRemove {
		fmt.Printf("Removing: %s\n", filepath.Base(file))
		if err := os.Remove(file); err != nil {
			common.PrintError("Failed to remove %s: %v\n", file, err)
		}
	}

	common.PrintSuccess("\nRemoved %d old baselines\n", len(toRemove))
	return nil
}

func extractTagFromBaseline(filename string) string {
	file, err := os.Open(filename)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Tag: ") {
			return strings.TrimPrefix(line, "Tag: ")
		}
		if strings.HasPrefix(line, "----") {
			break
		}
	}
	return ""
}

func runBenchstat(baseline, current string) error {
	cmd := exec.Command("benchstat", baseline, current)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func showSimpleDiff(baseline, current string) error {
	cmd := exec.Command("diff", "-u", baseline, current)
	output, _ := cmd.CombinedOutput()
	fmt.Print(string(output))
	return nil
}