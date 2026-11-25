package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/omode"
	"github.com/mimecast/dtail/internal/protocol"
)

// runControlLines runs the JSON control loop of a client with args over
// the request lines and returns the decoded lines it wrote and whether it
// ended the session.
func runControlLines(t *testing.T, args config.Args, requests ...string) (lines []map[string]any, quit bool) {
	t.Helper()
	var out bytes.Buffer
	c := &baseClient{Args: args, control: &controlWriter{}}
	c.control.open(&out)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := false
	if err := c.runJSONControl(ctx, func() { ended = true }, strings.NewReader(strings.Join(requests, "\n"))); err != nil {
		t.Fatalf("control loop: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("control output line %q: %v", line, err)
		}
		lines = append(lines, decoded)
	}
	return lines, ended
}

// TestControlLoopAnswersEveryRequest checks the loop of the JSON control
// channel: the ready event first, then exactly one response per request
// with the request's id, also for a request that is rejected, and the end
// of the session at quit, after which nothing is handled.
func TestControlLoopAnswersEveryRequest(t *testing.T) {
	lines, quit := runControlLines(t, config.Args{Mode: omode.MapClient, QueryBuilder: true},
		`{"id":1,"cmd":"help"}`,
		``,
		`{"id":"two","cmd":"show"}`,
		`{"id":3,"cmd":"nope"}`,
		`not json`,
		`{"id":5}`,
		`{"id":6,"cmd":"tail"}`,
		`{"id":7,"cmd":"reload"}`,
		`{"id":8,"cmd":"fields","tabel":"STATS"}`,
		`{"id":9,"cmd":"quit"}`,
		`{"id":10,"cmd":"show"}`)
	if !quit {
		t.Fatal("quit did not end the session")
	}
	if len(lines) != 10 {
		t.Fatalf("%d lines, want the ready event and 9 responses: %v", len(lines), lines)
	}
	ready := lines[0]
	if ready["event"] != "ready" || ready["protocol"] != float64(controlProtocol) || ready["workload"] != "builder" ||
		strings.Contains(toJSON(t, ready["commands"]), `"tail"`) {
		t.Fatalf("ready event: %v", ready)
	}
	wantIDs := []any{float64(1), "two", float64(3), nil, float64(5), float64(6), float64(7), float64(8), float64(9)}
	wantOK := []bool{true, true, false, false, false, false, false, false, true}
	// A misspelt field is rejected, with the request's id, not ignored.
	wantErrors := []string{"", "", `unknown command "nope"`, "invalid request", `no "cmd"`, `no "tail" command`,
		`reload requires "args"`, `unknown field "tabel"`, ""}
	for i, response := range lines[1:] {
		reason, _ := response["error"].(string)
		if response["id"] != wantIDs[i] || response["ok"] != wantOK[i] || !strings.Contains(reason, wantErrors[i]) ||
			(wantErrors[i] == "") != (reason == "") {
			t.Errorf("response %d = %v, want id %v ok %v error %q", i+1, response, wantIDs[i], wantOK[i], wantErrors[i])
		}
	}
	help := toJSON(t, lines[1]["result"])
	for _, want := range []string{`"fields":`, `"complete":`, `"quit":"end the session"`, `"text":"`} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %s: %s", want, help)
		}
	}
	if state := toJSON(t, lines[2]["result"]); !strings.Contains(state, `"workload":"builder"`) ||
		!strings.Contains(state, `"paused":false`) {
		t.Errorf("state: %s", state)
	}
}

// TestControlPauseAndResume checks that pause and resume drive the output
// pause, which show reports, and that a rejected request (a reload with an
// unknown flag) leaves the output paused.
func TestControlPauseAndResume(t *testing.T) {
	lines, _ := runControlLines(t, config.Args{Mode: omode.TailClient},
		`{"id":1,"cmd":"pause"}`, `{"id":2,"cmd":"show"}`, `{"id":3,"cmd":"reload","args":["--bogus"]}`,
		`{"id":4,"cmd":"show"}`, `{"id":5,"cmd":"resume"}`, `{"id":6,"cmd":"show"}`)
	if lines[3]["ok"] != false {
		t.Fatalf("reload with an unknown flag: %v", lines[3])
	}
	var got []string
	for _, i := range []int{1, 2, 4, 5, 6} {
		got = append(got, toJSON(t, lines[i]["result"].(map[string]any)["paused"]))
	}
	if want := []string{"true", "true", "true", "false", "false"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paused = %v, want %v", got, want)
	}
}

func toJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestControlWriter checks that lines written before the writer has its
// target are dropped, and that a line is one JSON value with <, > and &
// left as they are.
func TestControlWriter(t *testing.T) {
	writer := &controlWriter{}
	writer.write(controlResponse{OK: true})
	var out bytes.Buffer
	writer.open(&out)
	writer.write(controlResponse{ID: json.RawMessage(`7`), Error: `a < b && "c" > d`})
	if want := `{"id":7,"ok":false,"error":"a < b && \"c\" > d"}` + "\n"; out.String() != want {
		t.Fatalf("written %q, want %q", out.String(), want)
	}
	var none *controlWriter
	none.write(controlResponse{})
}

// TestNewControlResult checks the rows a result passes on: the query's
// limit, or controlResultMaxRows without one, with the total kept.
func TestNewControlResult(t *testing.T) {
	rows := make([][]string, controlResultMaxRows+5)
	for i := range rows {
		rows[i] = []string{"x"}
	}
	table := mapr.ResultTable{Columns: []string{"count($line)"}, Rows: rows}
	for query, want := range map[string]int{
		"select count($line)":         controlResultMaxRows,
		"select count($line) limit 3": 3,
	} {
		parsed, err := mapr.NewQuery(query, logging.NopLogger{})
		if err != nil {
			t.Fatal(err)
		}
		result := newControlResult(parsed, table, true)
		if len(result.Rows) != want || result.RowsTotal != len(rows) || !result.Truncated || !result.Final {
			t.Errorf("%s: %d rows of %d, truncated %v", query, len(result.Rows), result.RowsTotal, result.Truncated)
		}
	}
	parsed, err := mapr.NewQuery("select count($line)", logging.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if empty := toJSON(t, newControlResult(parsed, mapr.ResultTable{Columns: []string{"count($line)"}}, false)); !strings.Contains(empty, `"rows":[]`) {
		t.Errorf("empty result: %s", empty)
	}
}

// TestNewControlFields checks the catalog as the fields command returns it:
// everything, or one table (matched ignoring case) with the $variables of
// its lines only, and the note of an empty catalog.
func TestNewControlFields(t *testing.T) {
	now := time.Unix(1000, 0)
	snap := catalogSnapshot{
		DefaultLogFormat: "default",
		Servers:          []string{"srv1"},
		Updated:          now.Add(-2 * time.Second),
		Errors:           []catalogError{{Server: "srv1", File: "/x", Error: "probe: no file to read"}},
		Formats: map[string]catalogFormat{"default": {
			Tables: map[string]map[string][]string{"STATS": {"conns": {"1"}}, "ORDERS": {"item": {"pear"}}},
			TableVariables: map[string]map[string][]string{
				"STATS": {"$hostname": {"web1"}}, "ORDERS": {"$hour": {"07"}}},
			Variables: map[string][]string{"$hostname": {"web1"}, "$hour": {"07"}},
		}},
	}
	all := toJSON(t, newControlFields(snap, "", now))
	for _, want := range []string{`"ageSeconds":2`, `"STATS":{"fields":{"conns":["1"]},"variables":{"$hostname":["web1"]}}`,
		`"ORDERS":`, `"errors":[{"server":"srv1","file":"/x","error":"probe: no file to read"}]`,
		`"variables":{"$hostname":["web1"],"$hour":["07"]}`} {
		if !strings.Contains(all, want) {
			t.Errorf("fields lack %s: %s", want, all)
		}
	}
	one := toJSON(t, newControlFields(snap, "orders", now))
	if strings.Contains(one, "STATS") || !strings.Contains(one, `"ORDERS":{"fields":{"item":["pear"]},"variables":{"$hour":["07"]}}`) ||
		strings.Contains(one, `"$hostname"`) {
		t.Errorf("fields of one table: %s", one)
	}
	if missing := newControlFields(snap, "nope", now); len(missing.Formats) != 0 || !strings.Contains(missing.Note, "nope") {
		t.Errorf("fields of a table that is not there: %+v", missing)
	}
	if empty := newControlFields(catalogSnapshot{}, "", now); !strings.Contains(empty.Note, "no server answered") ||
		toJSON(t, empty.Servers) != "[]" {
		t.Errorf("empty catalog: %+v", empty)
	}
}

// TestNewControlListing checks a listing as the list command returns it:
// the names starting with the prefix's last part, with the path a --files
// value takes.
func TestNewControlListing(t *testing.T) {
	listing := pathListing{server: "srv1", truncated: true, entries: []protocol.ProbeEntry{
		{Name: "app.log"}, {Name: "archive", Dir: true}, {Name: "db.log"}}}
	got := newControlListing("/var/log/a", "a", listing)
	want := controlListing{Dir: "/var/log/", Server: "srv1", Truncated: true, Entries: []controlListEntry{
		{Name: "app.log", Path: "/var/log/app.log"}, {Name: "archive", Path: "/var/log/archive", Dir: true}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listing = %+v, want %+v", got, want)
	}
	if relative := newControlListing("ap", "ap", listing); relative.Dir != "" || relative.Entries[0].Path != "app.log" {
		t.Fatalf("listing of the working directory: %+v", relative)
	}
}

// TestStatsProgressSink checks that the progress sink of the stats gets
// every changed progress a server reported, and none before one did.
func TestStatsProgressSink(t *testing.T) {
	source := &fakeProgressSource{totals: progressTotals{Servers: 1}}
	s := newTailStats(1, nil, time.Millisecond, nil)
	s.setProgress(source.get, nil)
	var got []int64
	s.progressSink = func(report progressReport) { got = append(got, report.Bytes) }
	s.tickProgress()
	source.totals = totalsOf(10, 100, 1, 1)
	s.tickProgress()
	s.tickProgress()
	source.totals = totalsOf(40, 100, 1, 1)
	s.tickProgress()
	if want := []int64{10, 40}; !reflect.DeepEqual(got, want) {
		t.Fatalf("progress passed on = %v, want %v", got, want)
	}
	event := toJSON(t, newControlProgress(json.RawMessage(`4`), progressReport{progressTotals: totalsOf(40, 100, 1, 1)}))
	if want := `{"event":"progress","id":4,"bytes":40,"total":100,"percent":40,`; !strings.HasPrefix(event, want) {
		t.Fatalf("progress event %s, want prefix %s", event, want)
	}
}

// TestControlCompleteQueryListAndWait covers the handlers that read the
// catalog and wait for the servers, on a session without servers: complete
// completes a query and a control line as Tab does, query rejects a query
// that does not parse and answers the state for none, list answers from
// the cached listing and fails for a directory no server listed, and
// waitHeld waits for its channel, the timeout or the context.
func TestControlCompleteQueryListAndWait(t *testing.T) {
	now := time.Unix(1000, 0)
	prober := &interactiveProber{catalog: newFieldCatalog(), now: func() time.Time { return now },
		connections: func() []connectors.Connector { return nil }, files: func() []string { return []string{"/var/log/app.log"} }}
	prober.catalog.storeListing("srv1", "/var/log/", protocol.ProbeAnswer{Dir: "/var/log/",
		Entries: []protocol.ProbeEntry{{Name: "app.log"}, {Name: "old", Dir: true}}}, now)
	c := &baseClient{Args: config.Args{Mode: omode.TailClient, InteractiveQuery: true}, control: &controlWriter{},
		prober: prober}
	s := &controlSession{client: c, pause: c.newInteractivePause()}
	ctx := context.Background()
	run := func(request controlRequest) (string, error) {
		result, err := s.run(ctx, request)
		return toJSON(t, result), err
	}

	if got, err := run(controlRequest{Cmd: "complete", Query: "sel"}); err != nil ||
		!strings.Contains(got, `"line":"select ","cursor":7,"changed":true`) {
		t.Errorf("complete of a query: %s, %v", got, err)
	}
	if got, err := run(controlRequest{Cmd: "complete", Line: ":rel"}); err != nil || !strings.Contains(got, `"line":":reload "`) {
		t.Errorf("complete of a control line: %s, %v", got, err)
	}
	outside := 99
	if _, err := run(controlRequest{Cmd: "complete", Query: "sel", Cursor: &outside}); err == nil {
		t.Error("complete with a cursor outside the line was accepted")
	}
	if _, err := run(controlRequest{Cmd: "query", Query: "select count("}); err == nil || !strings.Contains(err.Error(), "invalid query") {
		t.Errorf("query that does not parse: %v", err)
	}
	if got, err := run(controlRequest{Cmd: "query"}); err != nil || !strings.Contains(got, `"workload":"tail"`) {
		t.Errorf("query without a query: %s, %v", got, err)
	}
	if got, err := run(controlRequest{Cmd: "list", Prefix: "/var/log/a"}); err != nil ||
		!strings.Contains(got, `"entries":[{"name":"app.log","path":"/var/log/app.log"}],"server":"srv1"`) {
		t.Errorf("list from the cache: %s, %v", got, err)
	}
	if _, err := run(controlRequest{Cmd: "list", Prefix: "/nowhere/"}); err == nil {
		t.Error("list of a directory no server listed succeeded")
	}

	closed := make(chan struct{})
	close(closed)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if !s.waitHeld(ctx, closed, time.Minute) || s.waitHeld(ctx, nil, time.Minute) ||
		s.waitHeld(ctx, make(chan struct{}), time.Millisecond) || s.waitHeld(cancelled, make(chan struct{}), time.Minute) {
		t.Error("waitHeld: want true for a closed channel only")
	}
}

// TestSinkResults checks the results a client with a result sink passes on
// instead of printing a table: an interim result only with rows and when
// it changed, the final one always, and nothing on the client's output.
func TestSinkResults(t *testing.T) {
	c, logger := newReportTestClient(t, DefaultMode, omode.MapClient, 1)
	var got []controlResult
	c.resultSink = func(result controlResult) { got = append(got, result) }

	report := func(final bool) {
		t.Helper()
		if err := c.reportResults(final); err != nil {
			t.Fatal(err)
		}
	}
	report(false) // no rows yet
	sendReportTestAggregate(t, c, "srv1", 3)
	report(false)
	report(false) // unchanged
	sendReportTestAggregate(t, c, "srv2", 4)
	report(true)
	report(true) // the final result is always passed on

	var rows []string
	for _, result := range got {
		rows = append(rows, fmt.Sprintf("%v final=%v", result.Rows, result.Final))
	}
	want := []string{"[[3]] final=false", "[[7]] final=true", "[[7]] final=true"}
	if !reflect.DeepEqual(rows, want) || got[0].Columns[0] != "count(foo)" || got[0].RowsTotal != 1 {
		t.Fatalf("results passed on = %v (first %+v), want %v", rows, got[0], want)
	}
	if printed := len(logger.tables()); printed != 0 {
		t.Fatalf("%d tables printed next to the result sink", printed)
	}
}
