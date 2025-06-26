package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	var (
		size   string
		output string
		format string
	)

	flag.StringVar(&size, "size", "10MB", "Size of the file (e.g., 10MB, 100MB, 1GB)")
	flag.StringVar(&output, "output", "test.log", "Output file path")
	flag.StringVar(&format, "format", "log", "Format: log or csv")
	flag.Parse()

	// Parse size
	sizeBytes, err := parseSize(size)
	if err != nil {
		log.Fatalf("Invalid size: %v", err)
	}

	// Generate data
	switch format {
	case "log":
		generateLogFile(output, sizeBytes)
	case "csv":
		generateCSVFile(output, sizeBytes)
	default:
		log.Fatalf("Unknown format: %s", format)
	}

	fmt.Printf("Generated %s file: %s\n", size, output)
}

func parseSize(size string) (int64, error) {
	size = strings.ToUpper(size)
	multiplier := int64(1)

	if strings.HasSuffix(size, "GB") {
		multiplier = 1024 * 1024 * 1024
		size = strings.TrimSuffix(size, "GB")
	} else if strings.HasSuffix(size, "MB") {
		multiplier = 1024 * 1024
		size = strings.TrimSuffix(size, "MB")
	} else if strings.HasSuffix(size, "KB") {
		multiplier = 1024
		size = strings.TrimSuffix(size, "KB")
	}

	base, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return 0, err
	}

	return base * multiplier, nil
}

func generateLogFile(filename string, targetSize int64) {
	f, err := os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Sample log lines
	logLevels := []string{"INFO", "WARN", "ERROR", "DEBUG"}
	actions := []string{
		"Processing request",
		"Handling connection",
		"Executing query",
		"Loading configuration",
		"Updating cache",
		"Validating input",
		"Sending response",
		"Checking permissions",
	}

	bytesWritten := int64(0)
	lineNum := 0
	startTime := time.Now()

	for bytesWritten < targetSize {
		lineNum++
		timestamp := startTime.Add(time.Duration(lineNum) * time.Millisecond).Format("2006-01-02 15:04:05.000")
		level := logLevels[rand.Intn(len(logLevels))]
		action := actions[rand.Intn(len(actions))]
		userID := rand.Intn(1000)
		requestID := fmt.Sprintf("req-%d", lineNum)
		duration := rand.Intn(5000)

		line := fmt.Sprintf("[%s] %s - %s for user%d (request: %s, duration: %dms)\n",
			timestamp, level, action, userID, requestID, duration)

		n, err := f.WriteString(line)
		if err != nil {
			log.Fatal(err)
		}
		bytesWritten += int64(n)

		// Add some variety with stack traces for errors
		if level == "ERROR" && rand.Float32() < 0.3 {
			stackTrace := fmt.Sprintf("  Stack trace:\n    at function1() file1.go:123\n    at function2() file2.go:456\n    at main() main.go:789\n")
			n, err := f.WriteString(stackTrace)
			if err != nil {
				log.Fatal(err)
			}
			bytesWritten += int64(n)
		}
	}
}

func generateCSVFile(filename string, targetSize int64) {
	f, err := os.Create(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	// Write header
	header := "timestamp,user,action,duration,status,category\n"
	f.WriteString(header)
	bytesWritten := int64(len(header))

	actions := []string{"login", "query", "update", "delete", "logout", "search", "export", "import"}
	statuses := []string{"success", "failure", "timeout", "pending"}
	categories := []string{"web", "api", "batch", "admin"}

	lineNum := 0
	startTime := time.Now()

	for bytesWritten < targetSize {
		lineNum++
		timestamp := startTime.Add(time.Duration(lineNum) * time.Second).Format("2006-01-02 15:04:05")
		user := fmt.Sprintf("user%d", rand.Intn(100))
		action := actions[rand.Intn(len(actions))]
		duration := 100 + rand.Intn(9900)
		status := statuses[rand.Intn(len(statuses))]
		category := categories[rand.Intn(len(categories))]

		line := fmt.Sprintf("%s,%s,%s,%d,%s,%s\n",
			timestamp, user, action, duration, status, category)

		n, err := f.WriteString(line)
		if err != nil {
			log.Fatal(err)
		}
		bytesWritten += int64(n)
	}
}