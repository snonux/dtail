package integrationtests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testDMapWithServer runs a DMap command with a running dserver and compares output
func testDMapWithServer(t *testing.T, args []string, csvFile, expectedCsvFile, queryFile, expectedQueryFile string) error {
	// Start dserver
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver in background
	dserverCtx, dserverCancel := context.WithCancel(ctx)
	defer dserverCancel()

	dserverStdout, dserverStderr, dserverErr, err := startCommand(dserverCtx, t, "", "../dserver", "--cfg", "none", "--port", "2222")
	if err != nil {
		return fmt.Errorf("failed to start dserver: %v", err)
	}

	// Wait for server to start
	time.Sleep(2 * time.Second)

	// Run dmap with server connection
	dmapArgs := append([]string{"--cfg", "none", "--servers", "localhost:2222", "--trustAllHosts"}, args...)
	dmapCtx, dmapCancel := context.WithTimeout(ctx, 30*time.Second)
	defer dmapCancel()

	dmapStdout, dmapStderr, dmapCmdErr, err := startCommand(dmapCtx, t, "", "../dmap", dmapArgs...)
	if err != nil {
		dserverCancel()
		return fmt.Errorf("failed to start dmap: %v", err)
	}

	// Wait for dmap to complete
	dmapDone := make(chan struct{})
	go func() {
		defer close(dmapDone)
		waitForCommand(dmapCtx, t, dmapStdout, dmapStderr, dmapCmdErr)
	}()

	// Wait for dmap completion or timeout
	select {
	case <-dmapDone:
		// DMap completed
	case <-dmapCtx.Done():
		dserverCancel()
		return fmt.Errorf("dmap command timed out")
	}

	// Stop the server
	dserverCancel()

	// Wait a bit for server cleanup
	time.Sleep(500 * time.Millisecond)

	// Drain server channels to avoid goroutine leaks
	go func() {
		for range dserverStdout {
		}
	}()
	go func() {
		for range dserverStderr {
		}
	}()
	go func() {
		for range dserverErr {
		}
	}()

	// Compare CSV output
	// For DMap tests, we need to use compareFilesContents for some tests
	if strings.Contains(expectedCsvFile, "dmap2") || 
	   strings.Contains(expectedCsvFile, "dmap3") || 
	   strings.Contains(expectedCsvFile, "dmap4") || 
	   strings.Contains(expectedCsvFile, "dmap5") {
		if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
			return fmt.Errorf("CSV file contents comparison failed: %v", err)
		}
	} else {
		if err := compareFiles(t, csvFile, expectedCsvFile); err != nil {
			return fmt.Errorf("CSV file comparison failed: %v", err)
		}
	}

	// Compare query file
	if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
		return fmt.Errorf("query file comparison failed: %v", err)
	}

	return nil
}

// testDMapMultipleRunsWithServer runs a DMap command multiple times with server (for append tests)
func testDMapMultipleRunsWithServer(t *testing.T, args []string, csvFile, expectedCsvFile, queryFile, expectedQueryFile string, numRuns int) error {
	// Start dserver
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start dserver in background
	dserverCtx, dserverCancel := context.WithCancel(ctx)
	defer dserverCancel()

	dserverStdout, dserverStderr, dserverErr, err := startCommand(dserverCtx, t, "", "../dserver", "--cfg", "none", "--port", "2222")
	if err != nil {
		return fmt.Errorf("failed to start dserver: %v", err)
	}

	// Wait for server to start
	time.Sleep(2 * time.Second)

	// Run dmap multiple times
	for i := 0; i < numRuns; i++ {
		dmapArgs := append([]string{"--cfg", "none", "--servers", "localhost:2222", "--trustAllHosts"}, args...)
		dmapCtx, dmapCancel := context.WithTimeout(ctx, 30*time.Second)

		dmapStdout, dmapStderr, dmapCmdErr, err := startCommand(dmapCtx, t, "", "../dmap", dmapArgs...)
		if err != nil {
			dmapCancel()
			dserverCancel()
			return fmt.Errorf("failed to start dmap (run %d): %v", i+1, err)
		}

		// Wait for dmap to complete
		dmapDone := make(chan struct{})
		go func() {
			defer close(dmapDone)
			waitForCommand(dmapCtx, t, dmapStdout, dmapStderr, dmapCmdErr)
		}()

		// Wait for dmap completion or timeout
		select {
		case <-dmapDone:
			// DMap completed
		case <-dmapCtx.Done():
			dmapCancel()
			dserverCancel()
			return fmt.Errorf("dmap command timed out (run %d)", i+1)
		}

		dmapCancel()
		// Small delay between runs
		time.Sleep(100 * time.Millisecond)
	}

	// Stop the server
	dserverCancel()

	// Wait a bit for server cleanup
	time.Sleep(500 * time.Millisecond)

	// Drain server channels to avoid goroutine leaks
	go func() {
		for range dserverStdout {
		}
	}()
	go func() {
		for range dserverStderr {
		}
	}()
	go func() {
		for range dserverErr {
		}
	}()

	// Compare output files
	if expectedCsvFile != "" {
		// For DMap tests, we need to use compareFilesContents for some tests
		if strings.Contains(expectedCsvFile, "dmap2") || 
		   strings.Contains(expectedCsvFile, "dmap3") || 
		   strings.Contains(expectedCsvFile, "dmap4") || 
		   strings.Contains(expectedCsvFile, "dmap5") {
			if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
				return fmt.Errorf("CSV file contents comparison failed: %v", err)
			}
		} else {
			if err := compareFiles(t, csvFile, expectedCsvFile); err != nil {
				return fmt.Errorf("CSV file comparison failed: %v", err)
			}
		}
	}

	if expectedQueryFile != "" {
		if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
			return fmt.Errorf("query file comparison failed: %v", err)
		}
	}

	return nil
}