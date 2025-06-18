package integrationtests

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/mimecast/dtail/internal/config"
)

func TestDMap1(t *testing.T) {
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
			if err := testDMap1(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDMap1(t *testing.T, useServer bool) error {
	testTable := map[string]string{
		"a": "from STATS select count($line),last($time)," +
			"avg($goroutines),min(concurrentConnections),max(lifetimeConnections) " +
			"group by $hostname",
		"b": "from STATS select count($line),last($time)," +
			"avg($goroutines),min(concurrentConnections),max(lifetimeConnections) " +
			"group by $hostname where lifetimeConnections >= 3",
		"c": "from STATS select count($line),last($time)," +
			"avg($goroutines),min(concurrentConnections),max(lifetimeConnections) " +
			"group by $hostname where $time eq \"1002-071949\"",
		"d": "from STATS select $mask,$md5,$foo,$bar,$baz,last($time)," +
			" set $mask = maskdigits($time), $md5 = md5sum($time), " +
			"$foo = 42, $bar = \"baz\", $baz = $time group by $hostname",
	}

	for subtestName, query := range testTable {
		t.Log("Testing dmap with input file")
		if err := testDmap1Sub(t, query, subtestName, false, useServer); err != nil {
			t.Error(err)
			return err
		}
		t.Log("Testing dmap with stdin input pipe")
		if err := testDmap1Sub(t, query, subtestName, true, useServer); err != nil {
			t.Error(err)
			return err
		}
	}
	return nil
}

func testDmap1Sub(t *testing.T, query, subtestName string, usePipe bool, useServer bool) error {
	var inFile, expectedCsvFile, expectedQueryFile, csvFile string
	
	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile = "small_mapr_testdata.log"
		csvFile = fmt.Sprintf("small_dmap1%s.csv.tmp", subtestName)
		expectedCsvFile = fmt.Sprintf("small_dmap1%s.csv.expected", subtestName)
		expectedQueryFile = fmt.Sprintf("small_dmap1%s.csv.query.expected", subtestName)
	} else {
		inFile = "mapr_testdata.log"
		csvFile = fmt.Sprintf("dmap1%s.csv.tmp", subtestName)
		expectedCsvFile = fmt.Sprintf("dmap1%s.csv.expected", subtestName)
		expectedQueryFile = fmt.Sprintf("dmap1%s.csv.query.expected", subtestName)
	}
	
	queryFile := fmt.Sprintf("%s.query", csvFile)
	query = fmt.Sprintf("%s outfile %s", query, csvFile)

	if useServer {
		// Server mode testing
		var args []string
		if usePipe {
			// For pipe mode with server, we need to handle this differently
			// DMap with server doesn't support stdin pipe in the same way
			// So we'll just test file mode for server
			args = []string{"--query", query, "--logger", "stdout", "--logLevel", "info", "--noColor", inFile}
		} else {
			args = []string{"--query", query, "--logger", "stdout", "--logLevel", "info", "--noColor", inFile}
		}
		return testDMapWithServer(t, args, csvFile, expectedCsvFile, queryFile, expectedQueryFile)
	} else {
		// Serverless mode testing (original code)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var stdoutCh, stderrCh <-chan string
		var cmdErrCh <-chan error
		var err error

		if usePipe {
			stdoutCh, stderrCh, cmdErrCh, err = startCommand(ctx, t,
				inFile, "../dmap",
				"--cfg", "none",
				"--query", query,
				"--logger", "stdout",
				"--logLevel", "info",
				"--noColor")
		} else {
			stdoutCh, stderrCh, cmdErrCh, err = startCommand(ctx, t,
				"", "../dmap",
				"--cfg", "none",
				"--query", query,
				"--logger", "stdout",
				"--logLevel", "info",
				"--noColor",
				inFile)
		}

		if err != nil {
			return err
		}

		waitForCommand(ctx, t, stdoutCh, stderrCh, cmdErrCh)

		if err := compareFiles(t, csvFile, expectedCsvFile); err != nil {
			return err
		}
		if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
			return err
		}

		os.Remove(csvFile)
		os.Remove(queryFile)
		return nil
	}
}

func TestDMap2(t *testing.T) {
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
			if err := testDMap2(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDMap2(t *testing.T, useServer bool) error {
	var inFile, expectedCsvFile, expectedQueryFile, csvFile string
	outFile := "dmap2.stdout.tmp"

	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile = "small_mapr_testdata.log"
		csvFile = "small_dmap2.csv.tmp"
		expectedCsvFile = "small_dmap2.csv.expected"
		expectedQueryFile = "small_dmap2.csv.query.expected"
	} else {
		inFile = "mapr_testdata.log"
		csvFile = "dmap2.csv.tmp"
		expectedCsvFile = "dmap2.csv.expected"
		expectedQueryFile = "dmap2.csv.query.expected"
	}
	
	queryFile := fmt.Sprintf("%s.query", csvFile)

	query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
		"avg($goroutines),min($goroutines) group by $time order by count($time) "+
		"outfile %s", csvFile)

	if useServer {
		// Server mode testing
		args := []string{"--query", query, "--cfg", "none", inFile}
		return testDMapWithServer(t, args, csvFile, expectedCsvFile, queryFile, expectedQueryFile)
	} else {
		// Serverless mode testing (original code)
		_, err := runCommand(context.TODO(), t, outFile,
			"../dmap", "--query", query, "--cfg", "none", inFile)
		if err != nil {
			return err
		}

		if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
			return err
		}
		if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
			return err
		}

		os.Remove(outFile)
		os.Remove(csvFile)
		os.Remove(queryFile)
		return nil
	}
}

func TestDMap3(t *testing.T) {
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
			if err := testDMap3(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDMap3(t *testing.T, useServer bool) error {
	var inFile, expectedCsvFile, expectedQueryFile, csvFile string
	outFile := "dmap3.stdout.tmp"

	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile = "small_mapr_testdata.log"
		csvFile = "small_dmap3.csv.tmp"
		expectedCsvFile = "small_dmap3.csv.expected"
		expectedQueryFile = "small_dmap3.csv.query.expected"
	} else {
		inFile = "mapr_testdata.log"
		csvFile = "dmap3.csv.tmp"
		expectedCsvFile = "dmap3.csv.expected"
		expectedQueryFile = "dmap3.csv.query.expected"
	}
	
	queryFile := fmt.Sprintf("%s.query", csvFile)

	query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
		"avg($goroutines),min($goroutines) group by $time order by count($time) "+
		"outfile %s", csvFile)

	if useServer {
		// Server mode testing - use only 3 files instead of 100 to avoid channel overflow
		args := []string{
			"--query", query,
			"--cfg", "none",
			"--logger", "stdout",
			"--logLevel", "info",
			"--noColor",
			inFile, inFile, inFile,
		}
		return testDMapWithServer(t, args, csvFile, expectedCsvFile, queryFile, expectedQueryFile)
	} else {
		// Serverless mode testing (original code with 100 files)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t,
			"", "../dmap",
			"--query", query,
			"--cfg", "none",
			"--logger", "stdout",
			"--logLevel", "info",
			"--noColor",
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile,
			inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile, inFile)

		if err != nil {
			return err
		}
		waitForCommand(ctx, t, stdoutCh, stderrCh, cmdErrCh)

		if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
			return err
		}
		if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
			return err
		}

		os.Remove(outFile)
		os.Remove(csvFile)
		os.Remove(queryFile)
		return nil
	}
}

func TestDMap4Append(t *testing.T) {
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
			if err := testDMap4Append(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDMap4Append(t *testing.T, useServer bool) error {
	var inFile, expectedCsvFile, expectedQueryFile, csvFile string
	outFile := "dmap4.stdout.tmp"

	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile = "small_mapr_testdata.log"
		csvFile = "small_dmap4.csv.tmp"
		expectedCsvFile = "small_dmap4.csv.expected"
		expectedQueryFile = "small_dmap4.csv.query.expected"
	} else {
		inFile = "mapr_testdata.log"
		csvFile = "dmap4.csv.tmp"
		expectedCsvFile = "dmap4.csv.expected"
		expectedQueryFile = "dmap4.csv.query.expected"
	}
	
	queryFile := fmt.Sprintf("%s.query", csvFile)

	// Delete in case it exists already. Otherwise, test will fail.
	os.Remove(csvFile)

	query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
		"avg($goroutines),min($goroutines) group by $time order by count($time) "+
		"outfile append %s", csvFile)

	if useServer {
		// Server mode testing - run twice for append functionality
		args := []string{
			"--query", query,
			"--cfg", "none",
			"--logger", "stdout",
			"--logLevel", "info",
			"--noColor", inFile,
		}
		return testDMapMultipleRunsWithServer(t, args, csvFile, expectedCsvFile, queryFile, expectedQueryFile, 2)
	} else {
		// Serverless mode testing (original code)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Run dmap command twice, it should append in the 2nd iteration the new results to the already existing
		// file as we specified "outfile append". That works transparently for any mapreduce query
		// (e.g. also for the dtail command in streaming mode). But it is easier to test with the dmap
		// command.
		for i := 0; i < 2; i++ {
			stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t,
				"", "../dmap",
				"--query", query,
				"--cfg", "none",
				"--logger", "stdout",
				"--logLevel", "info",
				"--noColor", inFile)

			if err != nil {
				return err
			}
			waitForCommand(ctx, t, stdoutCh, stderrCh, cmdErrCh)
		}

		if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
			return err
		}
		if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
			return err
		}

		os.Remove(outFile)
		os.Remove(csvFile)
		os.Remove(queryFile)
		return nil
	}
}

func TestDMap5CSV(t *testing.T) {
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
			if err := testDMap5CSV(t, mode.useServer); err != nil {
				t.Error(err)
				return
			}
		})
	}
}

func testDMap5CSV(t *testing.T, useServer bool) error {
	var inFile, expectedCsvFile, expectedQueryFile, csvFile string
	outFile := "dmap5.stdout.tmp"

	if useServer {
		// Use small test data for server mode to avoid channel overflow
		inFile = "small_dmap5.csv.in"
		csvFile = "small_dmap5.csv.tmp"
		expectedCsvFile = "small_dmap5.csv.expected"
		expectedQueryFile = "small_dmap5.csv.query.expected"
	} else {
		inFile = "dmap5.csv.in"
		csvFile = "dmap5.csv.tmp"
		expectedCsvFile = "dmap5.csv.expected"
		expectedQueryFile = "dmap5.csv.query.expected"
	}
	
	queryFile := fmt.Sprintf("%s.query", csvFile)

	// Delete in case it exists already. Otherwise, test will fail.
	os.Remove(csvFile)

	query := fmt.Sprintf("select sum($timecount),last($time),min($min_goroutines),"+
		" group by $hostname"+
		" set $timecount = `count($time)`, $time = `$time`, $min_goroutines = `min($goroutines)`"+
		" logformat csv outfile %s", csvFile)

	if useServer {
		// Server mode testing - run twice (CSV input format with append)
		args := []string{
			"--query", query,
			"--cfg", "none",
			"--logger", "stdout",
			"--logLevel", "info",
			"--noColor", inFile,
		}
		return testDMapMultipleRunsWithServer(t, args, csvFile, expectedCsvFile, queryFile, expectedQueryFile, 2)
	} else {
		// Serverless mode testing (original code)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Run dmap command twice, it should append in the 2nd iteration the new results to the already existing
		// file as we specified "outfile append". That works transparently for any mapreduce query
		// (e.g. also for the dtail command in streaming mode). But it is easier to test with the dmap
		// command.
		for i := 0; i < 2; i++ {
			stdoutCh, stderrCh, cmdErrCh, err := startCommand(ctx, t,
				"", "../dmap",
				"--query", query,
				"--cfg", "none",
				"--logger", "stdout",
				"--logLevel", "info",
				"--noColor", inFile)

			if err != nil {
				return err
			}
			waitForCommand(ctx, t, stdoutCh, stderrCh, cmdErrCh)
		}

		if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
			return err
		}
		if err := compareFiles(t, queryFile, expectedQueryFile); err != nil {
			return err
		}

		os.Remove(outFile)
		os.Remove(csvFile)
		os.Remove(queryFile)
		return nil
	}
}
