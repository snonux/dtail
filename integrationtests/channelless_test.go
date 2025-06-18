package integrationtests

import (
	"context"
	"os"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

func TestDGrepChannelless(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	
	// Test dgrep with channelless mode (now the default)
	inFile := "mapr_testdata.log"
	outFile := "dgrepchannelless.stdout.tmp"
	expectedOutFile := "dgrepcontext1.txt.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		"--after", "3",
		"--before", "3",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestDCatChannelless(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	
	// Test dcat with channelless mode (now the default)
	inFile := "dcat1a.txt"
	outFile := "dcatchannelless.stdout.tmp"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dcat",
		"--plain",
		"--cfg", "none",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, inFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}

func TestChannellessMode(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}
	
	// Test that channelless mode (now default) works correctly
	
	// Test grep
	inFile := "mapr_testdata.log"
	outFile := "grep_channelless.tmp"
	expectedOutFile := "dgrep1.txt.expected"

	_, err := runCommand(context.TODO(), t, outFile,
		"../dgrep",
		"--plain",
		"--cfg", "none",
		"--grep", "1002-071947",
		inFile)

	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFiles(t, outFile, expectedOutFile); err != nil {
		t.Error(err)
		return
	}

	os.Remove(outFile)
}