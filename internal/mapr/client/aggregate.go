package client

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/protocol"
)

// Aggregate mapreduce data on the DTail client side.
type Aggregate struct {
	// This represents aggregated data of a single remote server.
	group *mapr.GroupSet
	// Shared per-client session state.
	session *SessionState
	// The currently tracked shared generation.
	generation uint64
	// The server we aggregate the data for (logging and debugging purposes only)
	server string
}

// NewAggregate create new client aggregator.
func NewAggregate(server string, session *SessionState) *Aggregate {
	generation := uint64(0)
	if session != nil {
		generation = session.Snapshot().Generation
	}

	return &Aggregate{
		group:      mapr.NewGroupSet(),
		session:    session,
		generation: generation,
		server:     server,
	}
}

// Aggregate data from mapr log line into local (and global) group sets.
func (a *Aggregate) Aggregate(message string) error {
	if a.session == nil {
		return fmt.Errorf("missing client mapreduce session state")
	}

	snapshot := a.session.Snapshot()
	if snapshot.Query == nil || snapshot.GlobalGroup == nil {
		return fmt.Errorf("missing client mapreduce query state")
	}
	if snapshot.Generation != a.generation {
		a.group.InitSet()
		a.generation = snapshot.Generation
	}

	parts := strings.Split(message, protocol.AggregateDelimiter)
	if len(parts) < 4 {
		return fmt.Errorf("aggregate message without any real data")
	}

	groupKey := parts[0]
	samples, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("unable to parse sample count '%s': %w", parts[1], err)
	}

	fields := a.makeFields(parts[2:])
	set := a.group.GetSet(groupKey)
	var addedSamples bool

	for _, sc := range snapshot.Query.Select {
		if val, ok := fields[sc.FieldStorage]; ok {
			if err := set.Aggregate(sc.FieldStorage, sc.Operation, val, true); err != nil {
				dlog.Client.Error(err)
				continue
			}
			addedSamples = true
		}
	}
	if addedSamples {
		set.Samples += samples
	}

	// Merge data from group into global group.
	isMerged, err := snapshot.GlobalGroup.MergeNoblock(snapshot.Query, a.group)
	if err != nil {
		return fmt.Errorf("unable to merge aggregate data for server %s: %w", a.server, err)
	}
	if isMerged {
		// Re-init local group (make it empty again).
		a.group.InitSet()
	}
	return nil
}

// Create a map of key-value pairs from a part list such as ["foo=bar",  "bar=baz"].
func (a *Aggregate) makeFields(parts []string) map[string]string {
	fields := make(map[string]string, len(parts))
	for _, part := range parts {
		kv := strings.SplitN(part, protocol.AggregateKVDelimiter, 2)
		if len(kv) != 2 {
			continue
		}
		fields[kv[0]] = kv[1]
	}
	return fields
}
