package handlers

import (
	"context"
	"strings"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr/server"
)

// Map command implements the mapreduce command server side.
type mapCommand struct {
	aggregate *server.Aggregate
	server    *ServerHandler
}

// newMapCommand returns a new server side mapreduce command.
//
// The Aggregate is the one and only aggregate path for BOTH server mode and
// serverless: an Aggregate is always built, and the read commands feed it
// directly via AggregateProcessor (no aggregate line-channel). The former
// regular server.Aggregate has been deleted (task hv0), so there is no
// fallback branch.
func newMapCommand(serverHandler *ServerHandler, argc int,
	args []string) (mapCommand, *server.Aggregate, error) {

	m := mapCommand{server: serverHandler}
	queryStr := strings.Join(args[1:], " ")
	defaultLogFormat := ""
	if serverHandler.serverCfg != nil {
		defaultLogFormat = serverHandler.serverCfg.MapreduceLogFormat
	}

	dlog.Server.Info("Creating turbo aggregate for MapReduce", "query", queryStr)
	aggregate, err := server.NewAggregate(queryStr, defaultLogFormat)
	if err != nil {
		return m, nil, err
	}
	m.aggregate = aggregate
	return m, aggregate, nil
}

func (m *mapCommand) Start(ctx context.Context, aggregatedMessages chan<- string) {
	m.aggregate.Start(ctx, aggregatedMessages)
}
