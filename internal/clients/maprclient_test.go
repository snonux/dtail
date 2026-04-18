package clients

import (
	"testing"
	"time"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/mapr"
	maprclient "github.com/mimecast/dtail/internal/mapr/client"
	"github.com/mimecast/dtail/internal/omode"
)

func TestMaprClientCommitSessionSpecResetsSharedState(t *testing.T) {
	query := mustMaprClientQuery(t, "select count(status) from stats group by status")
	client := &MaprClient{
		baseClient: baseClient{
			mu:   newBaseClientMu(),
			Args: config.Args{Mode: omode.MapClient},
		},
		session: maprclient.NewSessionState(query),
		mode:    DefaultMode,
	}
	client.setRegexForQuery(query)

	initial := client.session.Snapshot()
	group := mapr.NewGroupSet()
	set := group.GetSet("ERROR")
	set.Samples = 1
	set.FValues[query.Select[0].FieldStorage] = 1
	if err := initial.GlobalGroup.Merge(query, group); err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	if changed, ok := client.session.CommitRenderedResult(initial.Generation, "old-result"); !ok || !changed {
		t.Fatalf("CommitRenderedResult() = changed:%v ok:%v, want changed and ok", changed, ok)
	}

	spec := SessionSpec{
		Query: "select count(status) from warnings group by status",
	}
	if err := client.commitSessionSpec(spec, 4); err != nil {
		t.Fatalf("commitSessionSpec() error = %v", err)
	}

	updated := client.session.Snapshot()
	if updated.Generation != 4 {
		t.Fatalf("generation = %d, want 4", updated.Generation)
	}
	if updated.Query == nil || updated.Query.RawQuery != spec.Query {
		t.Fatalf("unexpected query after commit: %#v", updated.Query)
	}
	if !updated.GlobalGroup.IsEmpty() {
		t.Fatalf("expected committed global group to be reset")
	}
	if updated.LastResult != "" {
		t.Fatalf("last result = %q, want empty", updated.LastResult)
	}
	if client.RegexStr != "\\|MAPREDUCE:WARNINGS\\|" {
		t.Fatalf("RegexStr = %q, want WARNINGS table regex", client.RegexStr)
	}

	sessionSpec, err := client.makeSessionSpec()
	if err != nil {
		t.Fatalf("makeSessionSpec() error = %v", err)
	}
	if sessionSpec.Query != spec.Query {
		t.Fatalf("session spec query = %q, want %q", sessionSpec.Query, spec.Query)
	}
}

func TestMaprClientCommitSessionSpecRejectsMissingQuery(t *testing.T) {
	query := mustMaprClientQuery(t, "select count(status) from stats group by status")
	client := &MaprClient{
		baseClient: baseClient{
			mu:   newBaseClientMu(),
			Args: config.Args{Mode: omode.MapClient},
		},
		session: maprclient.NewSessionState(query),
		mode:    DefaultMode,
	}

	if err := client.commitSessionSpec(SessionSpec{}, 2); err == nil {
		t.Fatalf("expected commitSessionSpec() to reject empty query")
	}
}

func TestMaprClientReportDelayUsesRampUpAndSteadyIntervals(t *testing.T) {
	query := mustMaprClientQuery(t, "select count(status) from stats group by status interval 8")
	client := &MaprClient{}

	if delay := client.reportDelay(query, true); delay != 4*time.Second {
		t.Fatalf("ramp-up delay = %v, want 4s", delay)
	}
	if delay := client.reportDelay(query, false); delay != 8*time.Second {
		t.Fatalf("steady delay = %v, want 8s", delay)
	}
}

func mustMaprClientQuery(t *testing.T, queryStr string) *mapr.Query {
	t.Helper()

	query, err := mapr.NewQuery(queryStr)
	if err != nil {
		t.Fatalf("NewQuery(%q) error = %v", queryStr, err)
	}
	return query
}
