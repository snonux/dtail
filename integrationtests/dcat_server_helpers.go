package integrationtests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// testDCatWithServer tests dcat command with a running server
func testDCatWithServer(t *testing.T, args []string, outFile, expectedFile string) error {
	
	port := getUniquePortNumber()
	bindAddress := "localhost"
	
	// Check if this is the colors test
	isColorsTest := false
	for _, arg := range args {
		if strings.Contains(arg, "dcatcolors.txt") {
			isColorsTest = true
			break
		}
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	serverCh, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout",
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		return fmt.Errorf("failed to start server: %v", err)
	}

	// Give server time to start
	time.Sleep(1 * time.Second)
	t.Log("Server should be started now")

	// Prepare dcat args with server connection
	dcatArgs := append([]string{
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
	}, args...)

	// Start dcat client
	t.Logf("Starting dcat with %d args: first few are %v", len(dcatArgs), dcatArgs[:min(10, len(dcatArgs))])
	clientCh, _, _, err := startCommand(ctx, t,
		"", "../dcat", dcatArgs...)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start dcat client: %v", err)
	}

	// Collect all output
	var output []string
	// For tests with many files (like TestDCat2 with 100 files), we need more time
	timeoutDuration := 30 * time.Second
	if len(args) > 50 {
		timeoutDuration = 120 * time.Second // 2 minutes for large tests
	}
	timeout := time.After(timeoutDuration)
	linesReceived := 0
	lastLineTime := time.Now()
	
	// Determine idle timeout based on test size
	idleTimeout := 500 * time.Millisecond
	if len(args) > 50 {
		idleTimeout = 2 * time.Second
	}
	
	for {
		// Check timeout before entering select
		if linesReceived > 0 && time.Since(lastLineTime) > idleTimeout {
			t.Logf("No new lines for %v after receiving %d lines, finishing (pre-select)", idleTimeout, linesReceived)
			goto done
		}
		
		select {
		case line := <-serverCh:
			// Only log important server errors, not routine messages
			if strings.Contains(line, "ERROR|") && 
			   !strings.Contains(line, "use of closed network connection") {
				t.Logf("server error: %s", line)
			}
		case line := <-clientCh:
			// Only log client errors if needed
			// Process empty lines too - they are valid content
			// Don't skip empty lines as they may be meaningful
			
			if strings.HasPrefix(line, "REMOTE|") {
				// For server mode tests, we need to extract the content from REMOTE| lines
				// Format: REMOTE|hostname|priority|lineno|sourceID|content
				parts := strings.Split(line, "|")
				if len(parts) >= 6 {
					// Join all parts after the 5th | as content (in case content has |)
					content := strings.Join(parts[5:], "|")
					if isColorsTest {
						// For colors test, the content is already the full expected line
						// The content might have trailing newlines, strip them
						content = strings.TrimRight(content, "\n")
						output = append(output, content)
					} else {
						// For other tests, we need to extract the actual file content
						// which is after the line number prefix
						if strings.Contains(content, " ") {
							contentParts := strings.SplitN(content, " ", 2)
							if len(contentParts) == 2 {
								output = append(output, contentParts[1])
							} else {
								output = append(output, content)
							}
						} else {
							output = append(output, content)
						}
					}
					linesReceived++
					lastLineTime = time.Now()
				}
			} else if strings.HasPrefix(line, "CLIENT|") {
				// Client status messages - ignore
				continue
			} else {
				// Direct content line from dcat (not wrapped in protocol)
				// Include empty lines as they are meaningful content
				output = append(output, line)
				linesReceived++
				lastLineTime = time.Now()
			}
		case <-timeout:
			// Timeout reached, finish collecting
			t.Logf("Main timeout reached after receiving %d lines", linesReceived)
			goto done
		case <-ctx.Done():
			goto done
		}
		
		// If we received some output and haven't seen new lines for a bit, we're probably done
		if linesReceived > 0 && time.Since(lastLineTime) > idleTimeout {
			t.Logf("No new lines for %v after receiving %d lines, finishing", idleTimeout, linesReceived)
			goto done
		}
	}

done:
	cancel()
	t.Logf("Collected %d lines of output", len(output))
	
	// Give server time to shut down properly
	time.Sleep(500 * time.Millisecond)
	
	// Write collected output to file
	if len(output) > 0 {
		fd, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %v", err)
		}
		defer fd.Close()
		
		// Write raw output preserving original line endings
		for i, chunk := range output {
			if i > 0 && isColorsTest {
				// For colors test, we need to add newlines between chunks
				fd.WriteString("\n")
			}
			fd.WriteString(chunk)
		}
	}

	// Compare results
	if err := compareFiles(t, outFile, expectedFile); err != nil {
		return err
	}

	os.Remove(outFile)
	return nil
}

// testDCatWithServerContents tests dcat command with a running server using content comparison
func testDCatWithServerContents(t *testing.T, args []string, outFile, expectedFile string) error {
	port := getUniquePortNumber()
	bindAddress := "localhost"
	
	// Check if this is the colors test
	isColorsTest := false
	for _, arg := range args {
		if strings.Contains(arg, "dcatcolors.txt") {
			isColorsTest = true
			break
		}
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server
	serverCh, _, _, err := startCommand(ctx, t,
		"", "../dserver",
		"--cfg", "none",
		"--logger", "stdout", 
		"--logLevel", "error",
		"--bindAddress", bindAddress,
		"--port", fmt.Sprintf("%d", port),
	)
	if err != nil {
		return fmt.Errorf("failed to start server: %v", err)
	}

	// Give server time to start
	time.Sleep(1 * time.Second)
	t.Log("Server should be started now")

	// Prepare dcat args with server connection
	dcatArgs := append([]string{
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
	}, args...)

	// Start dcat client
	t.Logf("Starting dcat with %d args: first few are %v", len(dcatArgs), dcatArgs[:min(10, len(dcatArgs))])
	clientCh, _, _, err := startCommand(ctx, t,
		"", "../dcat", dcatArgs...)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start dcat client: %v", err)
	}

	// Collect all output
	var output []string
	// For tests with many files (like TestDCat2 with 100 files), we need more time
	timeoutDuration := 30 * time.Second
	if len(args) > 50 {
		timeoutDuration = 120 * time.Second // 2 minutes for large tests
	}
	timeout := time.After(timeoutDuration)
	linesReceived := 0
	lastLineTime := time.Now()
	
	// Determine idle timeout based on test size
	idleTimeout := 500 * time.Millisecond
	if len(args) > 50 {
		idleTimeout = 2 * time.Second
	}
	
	for {
		// Check timeout before entering select
		if linesReceived > 0 && time.Since(lastLineTime) > idleTimeout {
			t.Logf("No new lines for %v after receiving %d lines, finishing (pre-select)", idleTimeout, linesReceived)
			goto done
		}
		
		select {
		case line := <-serverCh:
			// Only log important server errors, not routine messages
			if strings.Contains(line, "ERROR|") && 
			   !strings.Contains(line, "use of closed network connection") {
				t.Logf("server error: %s", line)
			}
		case line := <-clientCh:
			// Only log client errors if needed
			// Process empty lines too - they are valid content
			// Don't skip empty lines as they may be meaningful
			
			if strings.HasPrefix(line, "REMOTE|") {
				// For server mode tests, we need to extract the content from REMOTE| lines
				// Format: REMOTE|hostname|priority|lineno|sourceID|content
				parts := strings.Split(line, "|")
				if len(parts) >= 6 {
					// Join all parts after the 5th | as content (in case content has |)
					content := strings.Join(parts[5:], "|")
					if isColorsTest {
						// For colors test, the content is already the full expected line
						// The content might have trailing newlines, strip them
						content = strings.TrimRight(content, "\n")
						output = append(output, content)
					} else {
						// For other tests, we need to extract the actual file content
						// which is after the line number prefix
						if strings.Contains(content, " ") {
							contentParts := strings.SplitN(content, " ", 2)
							if len(contentParts) == 2 {
								output = append(output, contentParts[1])
							} else {
								output = append(output, content)
							}
						} else {
							output = append(output, content)
						}
					}
					linesReceived++
					lastLineTime = time.Now()
				}
			} else if strings.HasPrefix(line, "CLIENT|") {
				// Client status messages - ignore
				continue
			} else {
				// Direct content line from dcat (not wrapped in protocol)
				// Include empty lines as they are meaningful content
				output = append(output, line)
				linesReceived++
				lastLineTime = time.Now()
			}
		case <-timeout:
			// Timeout reached, finish collecting
			t.Logf("Main timeout reached after receiving %d lines", linesReceived)
			goto done
		case <-ctx.Done():
			goto done
		}
		
		// If we received some output and haven't seen new lines for a bit, we're probably done
		if linesReceived > 0 && time.Since(lastLineTime) > idleTimeout {
			t.Logf("No new lines for %v after receiving %d lines, finishing", idleTimeout, linesReceived)
			goto done
		}
	}

done:
	cancel()
	t.Logf("Collected %d lines of output", len(output))
	
	// Give server time to shut down properly
	time.Sleep(500 * time.Millisecond)
	
	// Write collected output to file
	if len(output) > 0 {
		fd, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %v", err)
		}
		defer fd.Close()
		
		// Write raw output preserving original line endings
		for i, chunk := range output {
			if i > 0 && isColorsTest {
				// For colors test, we need to add newlines between chunks
				fd.WriteString("\n")
			}
			fd.WriteString(chunk)
		}
	}

	// Compare results using content comparison
	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		return err
	}

	os.Remove(outFile)
	return nil
}