package handlers

import (
	"context"
	"strings"

	mapaggregate "github.com/mimecast/dtail/internal/mapr/aggregate"
)

// Map command implements the mapreduce command server side.
type mapCommand struct {
	aggregate *mapaggregate.Aggregate
	server    *ServerHandler
}

// newMapCommand returns a new server side mapreduce command.
//
// The Aggregate is the one and only aggregate path for BOTH server mode and
// serverless: an Aggregate is always built, and the read commands feed it
// directly via Processor (no aggregate line-channel). The former
// regular aggregate implementation has been deleted (task hv0), so there is no
// fallback branch.
func newMapCommand(serverHandler *ServerHandler, args []string) (mapCommand, *mapaggregate.Aggregate, error) {

	m := mapCommand{server: serverHandler}
	queryStr := strings.Join(args[1:], " ")
	defaultLogFormat := ""
	if serverHandler.serverCfg != nil {
		defaultLogFormat = serverHandler.serverCfg.MapreduceLogFormat
	}

	serverHandler.Logger().Info("Creating turbo aggregate for MapReduce", "query", queryStr)
	aggregate, err := mapaggregate.NewWithHostname(queryStr, defaultLogFormat,
		serverHandler.hostname, serverHandler.Logger())
	if err != nil {
		return m, nil, err
	}
	m.aggregate = aggregate
	return m, aggregate, nil
}

func (m *mapCommand) Start(ctx context.Context, aggregatedMessages chan<- string) {
	m.aggregate.Start(ctx, aggregatedMessages)
}
