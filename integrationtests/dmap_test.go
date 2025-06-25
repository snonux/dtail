package integrationtests

import (
	"fmt"
	"testing"
)

func TestDMap1(t *testing.T) {
	skipIfNotIntegrationTest(t)

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

	// Test in serverless mode
	t.Run("Serverless", func(t *testing.T) {
		for subtestName, query := range testTable {
			t.Run(subtestName, func(t *testing.T) {
				t.Log("Testing dmap with input file")
				testDmap1Serverless(t, query, subtestName, false)
				
				t.Log("Testing dmap with stdin input pipe")
				testDmap1Serverless(t, query, subtestName, true)
			})
		}
	})

	// Test in server mode
	t.Run("ServerMode", func(t *testing.T) {
		for subtestName, query := range testTable {
			t.Run(subtestName, func(t *testing.T) {
				t.Log("Testing dmap with input file in server mode")
				testDmap1WithServer(t, query, subtestName)
			})
		}
	})
}

func testDmap1Serverless(t *testing.T, query, subtestName string, usePipe bool) {
	paths := GetStandardTestPaths()
	csvFile := fmt.Sprintf("dmap1%s.csv.tmp", subtestName)
	expectedCsvFile := fmt.Sprintf("dmap1%s.csv.expected", subtestName)
	queryFile := fmt.Sprintf("%s.query", csvFile)
	query = fmt.Sprintf("%s outfile %s", query, csvFile)
	
	cleanupFiles(t, csvFile, queryFile)

	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()

	var stdoutCh, stderrCh <-chan string
	var cmdErrCh <-chan error
	var err error

	args := NewCommandArgs()
	args.Logger = "stdout"
	args.LogLevel = "info"
	args.NoColor = true
	args.ExtraArgs = []string{"--query", query}

	if usePipe {
		stdoutCh, stderrCh, cmdErrCh, err = startCommand(ctx, t,
			paths.MaprTestData, "../dmap", args.ToSlice()...)
	} else {
		stdoutCh, stderrCh, cmdErrCh, err = startCommand(ctx, t,
			"", "../dmap", append(args.ToSlice(), paths.MaprTestData)...)
	}

	if err != nil {
		t.Error(err)
		return
	}

	waitForCommand(ctx, t, stdoutCh, stderrCh, cmdErrCh)

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func testDmap1WithServer(t *testing.T, query, subtestName string) {
	paths := GetStandardTestPaths()
	csvFile := fmt.Sprintf("dmap1%s.csv.tmp", subtestName)
	expectedCsvFile := fmt.Sprintf("dmap1%s.csv.expected", subtestName)
	queryFile := fmt.Sprintf("%s.query", csvFile)
	query = fmt.Sprintf("%s outfile %s", query, csvFile)
	
	cleanupFiles(t, csvFile, queryFile)

	server := NewTestServer(t)
	if err := server.Start("error"); err != nil {
		t.Error(err)
		return
	}

	args := NewCommandArgs()
	args.Logger = "stdout"
	args.LogLevel = "info"
	args.NoColor = true
	args.Servers = []string{server.Address()}
	args.TrustAllHosts = true
	args.Files = []string{paths.MaprTestData}
	args.ExtraArgs = []string{"--query", query}

	stdoutCh, stderrCh, cmdErrCh, err := startCommand(server.ctx, t,
		"", "../dmap", args.ToSlice()...)
	if err != nil {
		t.Error(err)
		return
	}

	waitForCommand(server.ctx, t, stdoutCh, stderrCh, cmdErrCh)

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func TestDMap2(t *testing.T) {
	runDualModeTest(t, DualModeTest{
		Name:           "TestDMap2",
		ServerlessTest: testDMap2Serverless,
		ServerTest:     testDMap2WithServer,
	})
}

func testDMap2Serverless(t *testing.T) {
	paths := GetStandardTestPaths()
	outFile := "dmap2_serverless.stdout.tmp"
	csvFile := "dmap2_serverless.csv.tmp"
	expectedCsvFile := "dmap2.csv.expected"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	cleanupFiles(t, outFile, csvFile, queryFile)

	query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
		"avg($goroutines),min($goroutines) group by $time order by count($time) "+
		"outfile %s", csvFile)

	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	_, err := runCommand(ctx, t, outFile,
		"../dmap", "--query", query, "--cfg", "none", paths.MaprTestData)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func testDMap2WithServer(t *testing.T) {
	paths := GetStandardTestPaths()
	outFile := "dmap2_server.stdout.tmp"
	csvFile := "dmap2_server.csv.tmp"
	expectedCsvFile := "dmap2.csv.expected"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	cleanupFiles(t, outFile, csvFile, queryFile)

	server := NewTestServer(t)
	if err := server.Start("error"); err != nil {
		t.Error(err)
		return
	}

	query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
		"avg($goroutines),min($goroutines) group by $time order by count($time) "+
		"outfile %s", csvFile)

	args := NewCommandArgs()
	args.Servers = []string{server.Address()}
	args.TrustAllHosts = true
	args.NoColor = true
	args.Files = []string{paths.MaprTestData}
	args.ExtraArgs = []string{"--query", query}

	_, err := runCommand(server.ctx, t, outFile,
		"../dmap", args.ToSlice()...)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func TestDMap3(t *testing.T) {
	runDualModeTest(t, DualModeTest{
		Name:           "TestDMap3",
		ServerlessTest: testDMap3Serverless,
		ServerTest:     testDMap3WithServer,
	})
}

func testDMap3Serverless(t *testing.T) {
	paths := GetStandardTestPaths()
	outFile := "dmap3_serverless.stdout.tmp"
	csvFile := "dmap3_serverless.csv.tmp"
	expectedCsvFile := "dmap3.csv.expected"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	cleanupFiles(t, outFile, csvFile, queryFile)

	query := fmt.Sprintf("from STATS select $hostname,count($hostname),avg($queriesPerSecond) "+
		"group by $hostname order by avg($queriesPerSecond) limit 10 reverse interval 1 "+
		"outfile %s", csvFile)

	// Create a large list of input files
	var inputFiles []string
	for i := 0; i < 100; i++ {
		inputFiles = append(inputFiles, paths.MaprTestData)
	}

	// Simply run dmap with multiple input files directly
	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()

	args := NewCommandArgs()
	args.ExtraArgs = []string{"--query", query}
	
	_, err := runCommand(ctx, t, outFile,
		"../dmap", append(args.ToSlice(), inputFiles...)...)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func testDMap3WithServer(t *testing.T) {
	paths := GetStandardTestPaths()
	outFile := "dmap3_server.stdout.tmp"
	csvFile := "dmap3_server.csv.tmp"
	expectedCsvFile := "dmap3.csv.expected"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	cleanupFiles(t, outFile, csvFile, queryFile)

	server := NewTestServer(t)
	if err := server.Start("error"); err != nil {
		t.Error(err)
		return
	}

	query := fmt.Sprintf("from STATS select $hostname,count($hostname),avg($queriesPerSecond) "+
		"group by $hostname order by avg($queriesPerSecond) limit 10 reverse interval 1 "+
		"outfile %s", csvFile)

	// Create a large list of input files
	var inputFiles []string
	for i := 0; i < 100; i++ {
		inputFiles = append(inputFiles, paths.MaprTestData)
	}

	args := NewCommandArgs()
	args.Servers = []string{server.Address()}
	args.TrustAllHosts = true
	args.NoColor = true
	args.Files = inputFiles
	args.ExtraArgs = []string{"--query", query}

	_, err := runCommand(server.ctx, t, outFile,
		"../dmap", args.ToSlice()...)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func TestDMap4Append(t *testing.T) {
	runDualModeTest(t, DualModeTest{
		Name:           "TestDMap4Append",
		ServerlessTest: testDMap4AppendServerless,
		ServerTest:     testDMap4AppendWithServer,
	})
}

func testDMap4AppendServerless(t *testing.T) {
	paths := GetStandardTestPaths()
	csvFile := "dmap4_serverless.csv.tmp"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	
	// Clean up files once at the beginning
	cleanupFiles(t, csvFile, queryFile)
	
	t.Run("FirstQuery", func(t *testing.T) {
		stdout := "dmap4_serverless.stdout1.tmp"
		cleanupFiles(t, stdout)
		
		// First query
		query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
			"avg($goroutines),min($goroutines) group by $time order by count($time) "+
			"outfile %s", csvFile)

		ctx, cancel := createTestContextWithTimeout(t)
		defer cancel()
		_, err := runCommand(ctx, t, stdout,
			"../dmap", "--query", query, "--cfg", "none", paths.MaprTestData)
		if err != nil {
			t.Error(err)
			return
		}
		
		// Verify the CSV output
		if err := compareFilesContents(t, csvFile, "dmap4_query1.csv.expected"); err != nil {
			t.Error(err)
		}
		
		// Verify the query file
		if err := verifyQueryFile(t, queryFile, query); err != nil {
			t.Error(err)
		}
	})

	t.Run("SecondQueryWithAppend", func(t *testing.T) {
		stdout := "dmap4_serverless.stdout2.tmp"
		cleanupFiles(t, stdout)
		
		// Second query with append
		query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
			"avg($goroutines),min($goroutines) group by $time order by avg($goroutines) reverse "+
			"outfile append:%s", csvFile)

		ctx, cancel := createTestContextWithTimeout(t)
		defer cancel()
		_, err := runCommand(ctx, t, stdout,
			"../dmap", "--query", query, "--cfg", "none", paths.MaprTestData)
		if err != nil {
			t.Error(err)
			return
		}
		
		// Verify the CSV output (should still be the first query result - append doesn't change existing file)
		if err := compareFilesContents(t, csvFile, "dmap4_query1.csv.expected"); err != nil {
			t.Error(err)
		}
	})

	t.Run("ThirdQueryWithAppend", func(t *testing.T) {
		stdout := "dmap4_serverless.stdout3.tmp"
		cleanupFiles(t, stdout)
		
		// Third query with append (different structure)
		query := fmt.Sprintf("from STATS select count($line),$hostname "+
			"group by $hostname "+
			"outfile append:%s", csvFile)

		ctx, cancel := createTestContextWithTimeout(t)
		defer cancel()
		_, err := runCommand(ctx, t, stdout,
			"../dmap", "--query", query, "--cfg", "none", paths.MaprTestData)
		if err != nil {
			t.Error(err)
			return
		}
		
		// Verify the CSV output (should still be the first query result - append doesn't change existing file)
		if err := compareFilesContents(t, csvFile, "dmap4_query1.csv.expected"); err != nil {
			t.Error(err)
		}
		
		// For append test, the query file should still contain the first query
		firstQuery := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
			"avg($goroutines),min($goroutines) group by $time order by count($time) "+
			"outfile %s", csvFile)
		if err := verifyQueryFile(t, queryFile, firstQuery); err != nil {
			t.Error(err)
		}
	})
}

func testDMap4AppendWithServer(t *testing.T) {
	paths := GetStandardTestPaths()
	csvFile := "dmap4_server.csv.tmp"
	queryFile := fmt.Sprintf("%s.query", csvFile)

	server := NewTestServer(t)
	if err := server.Start("error"); err != nil {
		t.Error(err)
		return
	}

	baseArgs := NewCommandArgs()
	baseArgs.Servers = []string{server.Address()}
	baseArgs.TrustAllHosts = true
	baseArgs.NoColor = true
	baseArgs.Files = []string{paths.MaprTestData}

	// Clean up files once at the beginning
	cleanupFiles(t, csvFile, queryFile)

	t.Run("FirstQuery", func(t *testing.T) {
		stdout := "dmap4_server.stdout1.tmp"
		cleanupFiles(t, stdout)
		
		// First query
		query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
			"avg($goroutines),min($goroutines) group by $time order by count($time) "+
			"outfile %s", csvFile)
		
		args := *baseArgs
		args.ExtraArgs = []string{"--query", query}

		_, err := runCommand(server.ctx, t, stdout,
			"../dmap", args.ToSlice()...)
		if err != nil {
			t.Error(err)
			return
		}
		
		// Verify the CSV output
		if err := compareFilesContents(t, csvFile, "dmap4_query1.csv.expected"); err != nil {
			t.Error(err)
		}
		
		// Verify the query file
		if err := verifyQueryFile(t, queryFile, query); err != nil {
			t.Error(err)
		}
	})

	t.Run("SecondQueryWithAppend", func(t *testing.T) {
		stdout := "dmap4_server.stdout2.tmp"
		cleanupFiles(t, stdout)
		
		// Second query with append
		query := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
			"avg($goroutines),min($goroutines) group by $time order by avg($goroutines) reverse "+
			"outfile append:%s", csvFile)
		
		args := *baseArgs
		args.ExtraArgs = []string{"--query", query}

		_, err := runCommand(server.ctx, t, stdout,
			"../dmap", args.ToSlice()...)
		if err != nil {
			t.Error(err)
			return
		}
		
		// Verify the CSV output (should still be the first query result - append doesn't change existing file)
		if err := compareFilesContents(t, csvFile, "dmap4_query1.csv.expected"); err != nil {
			t.Error(err)
		}
	})

	t.Run("ThirdQueryWithAppend", func(t *testing.T) {
		stdout := "dmap4_server.stdout3.tmp"
		cleanupFiles(t, stdout)
		
		// Third query with append (different structure)
		query := fmt.Sprintf("from STATS select count($line),$hostname "+
			"group by $hostname "+
			"outfile append:%s", csvFile)
		
		args := *baseArgs
		args.ExtraArgs = []string{"--query", query}

		_, err := runCommand(server.ctx, t, stdout,
			"../dmap", args.ToSlice()...)
		if err != nil {
			t.Error(err)
			return
		}
		
		// Verify the CSV output (should still be the first query result - append doesn't change existing file)
		if err := compareFilesContents(t, csvFile, "dmap4_query1.csv.expected"); err != nil {
			t.Error(err)
		}
		
		// For append test, the query file should still contain the first query
		firstQuery := fmt.Sprintf("from STATS select count($time),$time,max($goroutines),"+
			"avg($goroutines),min($goroutines) group by $time order by count($time) "+
			"outfile %s", csvFile)
		if err := verifyQueryFile(t, queryFile, firstQuery); err != nil {
			t.Error(err)
		}
	})
}

func TestDMap5CSV(t *testing.T) {
	runDualModeTest(t, DualModeTest{
		Name:           "TestDMap5CSV",
		ServerlessTest: testDMap5CSVServerless,
		ServerTest:     testDMap5CSVWithServer,
	})
}

func testDMap5CSVServerless(t *testing.T) {
	inFile := "dmap5.csv.in"
	csvFile := "dmap5_serverless.csv.tmp"
	expectedCsvFile := "dmap5.csv.expected"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	outFile := "dmap5_serverless.stdout.tmp"
	cleanupFiles(t, csvFile, queryFile, outFile)

	query := fmt.Sprintf("select sum($timecount),last($time),min($min_goroutines) "+
		"group by $hostname set $timecount = `count($time)`, $time = `$time`, "+
		"$min_goroutines = `min($goroutines)` logformat csv outfile %s", csvFile)

	ctx, cancel := createTestContextWithTimeout(t)
	defer cancel()
	_, err := runCommand(ctx, t, outFile,
		"../dmap", "--query", query, "--cfg", "none", inFile)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	// Verify the query file contains the expected query
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}

func testDMap5CSVWithServer(t *testing.T) {
	inFile := "dmap5.csv.in"
	csvFile := "dmap5_server.csv.tmp"
	expectedCsvFile := "dmap5.csv.expected"
	queryFile := fmt.Sprintf("%s.query", csvFile)
	outFile := "dmap5_server.stdout.tmp"
	cleanupFiles(t, csvFile, queryFile, outFile)

	server := NewTestServer(t)
	if err := server.Start("error"); err != nil {
		t.Error(err)
		return
	}

	query := fmt.Sprintf("select sum($timecount),last($time),min($min_goroutines) "+
		"group by $hostname set $timecount = `count($time)`, $time = `$time`, "+
		"$min_goroutines = `min($goroutines)` logformat csv outfile %s", csvFile)

	args := NewCommandArgs()
	args.Servers = []string{server.Address()}
	args.TrustAllHosts = true
	args.NoColor = true
	args.Files = []string{inFile}
	args.ExtraArgs = []string{"--query", query}

	_, err := runCommand(server.ctx, t, outFile,
		"../dmap", args.ToSlice()...)
	if err != nil {
		t.Error(err)
		return
	}

	if err := compareFilesContents(t, csvFile, expectedCsvFile); err != nil {
		t.Error(err)
	}
	// Verify the query file contains the expected query
	if err := verifyQueryFile(t, queryFile, query); err != nil {
		t.Error(err)
	}
}