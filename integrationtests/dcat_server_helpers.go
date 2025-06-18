package integrationtests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// testDCatWithServer tests dcat command with a running server
func testDCatWithServer(t *testing.T, args []string, outFile, expectedFile string) error {
	port := getUniquePortNumber()
	bindAddress := "localhost"
	
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

	// Prepare dcat args with server connection
	dcatArgs := append([]string{
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
	}, args...)

	// Start dcat client
	clientCh, _, _, err := startCommand(ctx, t,
		"", "../dcat", dcatArgs...)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start dcat client: %v", err)
	}

	// Collect all output
	var output []string
	timeout := time.After(15 * time.Second)
	linesReceived := 0
	
	for {
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
				// Extract the actual content from DTail protocol format
				// Format: REMOTE|hostname|priority|lineno|sourceID|hostname|filename|linenum|content
				parts := strings.Split(line, "|")
				if len(parts) >= 8 {
					content := strings.Join(parts[7:], "|")
					// Remove line number prefix if present (from DTail format)
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
					linesReceived++
				}
			} else if strings.HasPrefix(line, "CLIENT|") {
				// Client status messages - ignore
				continue
			} else {
				// Direct content line from dcat (not wrapped in protocol)
				// Include empty lines as they are meaningful content
				output = append(output, line)
				linesReceived++
			}
		case <-timeout:
			// Timeout reached, finish collecting
			goto done
		case <-ctx.Done():
			goto done
		}
		
		// If we received some output and haven't seen new lines for a bit, we're probably done
		if linesReceived > 0 {
			select {
			case <-time.After(500 * time.Millisecond):
				goto done
			default:
				continue
			}
		}
	}

done:
	cancel()
	
	// Write collected output to file
	if len(output) > 0 {
		fd, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %v", err)
		}
		defer fd.Close()
		
		// Check if the expected file ends with a newline to match format
		expectedData, err := os.ReadFile(expectedFile)
		if err != nil {
			return fmt.Errorf("failed to read expected file: %v", err)
		}
		
		endsWithNewline := len(expectedData) > 0 && expectedData[len(expectedData)-1] == '\n'
		
		for i, line := range output {
			if i == len(output)-1 && !endsWithNewline {
				// Last line and original doesn't end with newline
				fd.WriteString(line)
			} else {
				fd.WriteString(line + "\n")
			}
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

	// Prepare dcat args with server connection
	dcatArgs := append([]string{
		"--servers", fmt.Sprintf("%s:%d", bindAddress, port),
		"--trustAllHosts",
		"--noColor",
	}, args...)

	// Start dcat client
	clientCh, _, _, err := startCommand(ctx, t,
		"", "../dcat", dcatArgs...)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start dcat client: %v", err)
	}

	// Collect all output
	var output []string
	timeout := time.After(15 * time.Second)
	linesReceived := 0
	
	for {
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
				// Extract the actual content from DTail protocol format
				// Format: REMOTE|hostname|priority|lineno|sourceID|hostname|filename|linenum|content
				parts := strings.Split(line, "|")
				if len(parts) >= 8 {
					content := strings.Join(parts[7:], "|")
					// Remove line number prefix if present (from DTail format)
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
					linesReceived++
				}
			} else if strings.HasPrefix(line, "CLIENT|") {
				// Client status messages - ignore
				continue
			} else {
				// Direct content line from dcat (not wrapped in protocol)
				// Include empty lines as they are meaningful content
				output = append(output, line)
				linesReceived++
			}
		case <-timeout:
			// Timeout reached, finish collecting
			goto done
		case <-ctx.Done():
			goto done
		}
		
		// If we received some output and haven't seen new lines for a bit, we're probably done
		if linesReceived > 0 {
			select {
			case <-time.After(500 * time.Millisecond):
				goto done
			default:
				continue
			}
		}
	}

done:
	cancel()
	
	// Write collected output to file
	if len(output) > 0 {
		fd, err := os.Create(outFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %v", err)
		}
		defer fd.Close()
		
		// Check if the expected file ends with a newline to match format
		expectedData, err := os.ReadFile(expectedFile)
		if err != nil {
			return fmt.Errorf("failed to read expected file: %v", err)
		}
		
		endsWithNewline := len(expectedData) > 0 && expectedData[len(expectedData)-1] == '\n'
		
		for i, line := range output {
			if i == len(output)-1 && !endsWithNewline {
				// Last line and original doesn't end with newline
				fd.WriteString(line)
			} else {
				fd.WriteString(line + "\n")
			}
		}
	}

	// Compare results using content comparison
	if err := compareFilesContents(t, outFile, expectedFile); err != nil {
		return err
	}

	os.Remove(outFile)
	return nil
}