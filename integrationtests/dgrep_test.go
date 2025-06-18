package integrationtests

import (
	"context"
	"os"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

func TestDGrep1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test both serverless and server modes
	modes := []struct {
		name string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}
	
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if err := testDGrep1(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDGrep1(t *testing.T, useServer bool) error {
	outFile := "dgrep.stdout.tmp"
	
	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile := "small_mapr_testdata.log"
		expectedOutFile := "small_dgrep1.txt.expected"
		args := []string{"--plain", "--cfg", "none", "--grep", "1002-071947", inFile}
		return testDGrepWithServer(t, args, outFile, expectedOutFile)
	} else {
		inFile := "mapr_testdata.log"
		expectedOutFile := "dgrep1.txt.expected"
		
		_, err := runCommand(context.TODO(), t, outFile,
			"../dgrep",
			"--plain",
			"--cfg", "none",
			"--grep", "1002-071947",
			inFile)

		if err != nil {
			return err
		}

		if err := compareFiles(t, outFile, expectedOutFile); err != nil {
			return err
		}

		os.Remove(outFile)
		return nil
	}
}

func TestDGrep2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test both serverless and server modes
	modes := []struct {
		name string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}
	
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if err := testDGrep2(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDGrep2(t *testing.T, useServer bool) error {
	outFile := "dgrep2.stdout.tmp"
	
	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile := "small_mapr_testdata.log"
		expectedOutFile := "small_dgrep2.txt.expected"
		args := []string{"--plain", "--cfg", "none", "--grep", "1002-071947", "--invert", inFile}
		return testDGrepWithServer(t, args, outFile, expectedOutFile)
	} else {
		inFile := "mapr_testdata.log"
		expectedOutFile := "dgrep2.txt.expected"
		
		_, err := runCommand(context.TODO(), t, outFile,
			"../dgrep",
			"--plain",
			"--cfg", "none",
			"--grep", "1002-071947",
			"--invert",
			inFile)

		if err != nil {
			return err
		}

		if err := compareFiles(t, outFile, expectedOutFile); err != nil {
			return err
		}

		os.Remove(outFile)
		return nil
	}
}

func TestDGrepContext1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test both serverless and server modes
	modes := []struct {
		name string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}
	
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if err := testDGrepContext1(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDGrepContext1(t *testing.T, useServer bool) error {
	outFile := "dgrepcontext1.stdout.tmp"
	
	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile := "small_mapr_testdata.log"
		expectedOutFile := "small_dgrepcontext1.txt.expected"
		args := []string{"--plain", "--cfg", "none", "--grep", "1002-071947", "--after", "3", "--before", "3", inFile}
		return testDGrepWithServer(t, args, outFile, expectedOutFile)
	} else {
		inFile := "mapr_testdata.log"
		expectedOutFile := "dgrepcontext1.txt.expected"
		
		_, err := runCommand(context.TODO(), t, outFile,
			"../dgrep",
			"--plain",
			"--cfg", "none",
			"--grep", "1002-071947",
			"--after", "3",
			"--before", "3", inFile)

		if err != nil {
			return err
		}

		if err := compareFiles(t, outFile, expectedOutFile); err != nil {
			return err
		}

		os.Remove(outFile)
		return nil
	}
}

func TestDGrepContext2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	// Test both serverless and server modes
	modes := []struct {
		name string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}
	
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if err := testDGrepContext2(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDGrepContext2(t *testing.T, useServer bool) error {
	outFile := "dgrepcontext2.stdout.tmp"
	
	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile := "small_mapr_testdata.log"
		expectedOutFile := "small_dgrepcontext2.txt.expected"
		args := []string{"--plain", "--cfg", "none", "--grep", "1002", "--max", "3", inFile}
		return testDGrepWithServer(t, args, outFile, expectedOutFile)
	} else {
		inFile := "mapr_testdata.log"
		expectedOutFile := "dgrepcontext2.txt.expected"
		
		_, err := runCommand(context.TODO(), t, outFile,
			"../dgrep",
			"--plain",
			"--cfg", "none",
			"--grep", "1002",
			"--max", "3",
			inFile)

		if err != nil {
			return err
		}

		if err := compareFiles(t, outFile, expectedOutFile); err != nil {
			return err
		}

		os.Remove(outFile)
		return nil
	}
}
