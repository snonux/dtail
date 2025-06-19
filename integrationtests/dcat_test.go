package integrationtests

import (
	"context"
	"os"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

func TestDCat1(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		t.Log("Skipping")
		return
	}

	inFiles := []string{"dcat1a.txt", "dcat1b.txt", "dcat1c.txt", "dcat1d.txt"}

	// Test both serverless and server modes
	modes := []struct {
		name      string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			// Test all files in both modes, restarting server for each file in server mode
			for _, inFile := range inFiles {
				if err := testDCat1(t, inFile, mode.useServer); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
}

func testDCat1(t *testing.T, inFile string, useServer bool) error {
	outFile := "dcat1.out"

	if useServer {
		// Now that channel buffer issue is fixed, use the actual test file
		return testDCatWithServer(t, []string{"--plain", "--cfg", "none", "--quiet", inFile}, outFile, inFile)
	} else {
		_, err := runCommand(context.TODO(), t, outFile,
			"../dcat", "--plain", "--cfg", "none", inFile)
		if err != nil {
			return err
		}
		if err := compareFiles(t, outFile, inFile); err != nil {
			return err
		}

		os.Remove(outFile)
		return nil
	}
}

func TestDCat2(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return
	}
	inFile := "dcat2.txt"
	expectedFile := "dcat2.txt.expected"
	outFile := "dcat2.out"

	args := []string{"--plain", "--logLevel", "error", "--cfg", "none"}

	// Cat file 100 times in one session.
	for i := 0; i < 100; i++ {
		args = append(args, inFile)
	}

	// Test both serverless and server modes
	modes := []struct {
		name      string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if mode.useServer {
				// Now that channel buffer issue is fixed, enable server mode for TestDCat2
				if err := testDCatWithServer(t, args, outFile, expectedFile); err != nil {
					t.Error(err)
					return
				}
			} else {
				_, err := runCommand(context.TODO(), t, outFile, "../dcat", args...)
				if err != nil {
					t.Error(err)
					return
				}

				if err := compareFilesContents(t, outFile, expectedFile); err != nil {
					t.Error(err)
					return
				}

				os.Remove(outFile)
			}
		})
	}
}

func TestDCat3(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return
	}
	inFile := "dcat3.txt"
	expectedFile := "dcat3.txt.expected"
	outFile := "dcat3.out"

	args := []string{"--plain", "--logLevel", "error", "--cfg", "none", inFile}

	// Test both serverless and server modes
	modes := []struct {
		name      string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if mode.useServer {
				if err := testDCatWithServerContents(t, args, outFile, expectedFile); err != nil {
					t.Error(err)
				}
			} else {
				// Notice, with DTAIL_INTEGRATION_TEST_RUN_MODE the DTail max line length is set
				// to 1024!
				_, err := runCommand(context.TODO(), t, outFile, "../dcat", args...)
				if err != nil {
					t.Error(err)
					return
				}

				if err := compareFilesContents(t, outFile, expectedFile); err != nil {
					t.Error(err)
					return
				}

				os.Remove(outFile)
			}
		})
	}
}

func TestDCatColors(t *testing.T) {
	if !config.Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return
	}

	inFile := "dcatcolors.txt"
	outFile := "dcatcolors.out"
	expectedFile := "dcatcolors.expected"
	args := []string{"--logLevel", "error", "--cfg", "none", inFile}

	// Test both serverless and server modes
	modes := []struct {
		name      string
		useServer bool
	}{
		{"Serverless", false},
		{"WithServer", true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			if mode.useServer {
				// Now that channel buffer issue is fixed, enable server mode for TestDCatColors
				if err := testDCatWithServer(t, args, outFile, expectedFile); err != nil {
					t.Error(err)
					return
				}
			} else {
				_, err := runCommand(context.TODO(), t, outFile, "../dcat", args...)

				if err != nil {
					t.Error(err)
					return
				}

				if err := compareFiles(t, outFile, expectedFile); err != nil {
					t.Error(err)
					return
				}

				os.Remove(outFile)
			}
		})
	}
}
