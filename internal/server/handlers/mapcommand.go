package handlers

import (
	"context"
	"strings"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr/server"
)

// Map command implements the mapreduce command server side.
type mapCommand struct {
	aggregate      *server.Aggregate
	turboAggregate *server.TurboAggregate
	server         *ServerHandler
}

// NewMapCommand returns a new server side mapreduce command.
func newMapCommand(serverHandler *ServerHandler, argc int,
	args []string) (mapCommand, *server.Aggregate, *server.TurboAggregate, error) {

	m := mapCommand{server: serverHandler}
	queryStr := strings.Join(args[1:], " ")
	defaultLogFormat := ""
	if serverHandler.serverCfg != nil {
		defaultLogFormat = serverHandler.serverCfg.MapreduceLogFormat
	}

	// If turbo boost is not disabled AND we're in server mode (not serverless), create a TurboAggregate
	// Turbo boost is enabled by default and is a server-side optimization
	dlog.Server.Debug("MapReduce mode check", "turboBoostDisable", serverHandler.serverCfg.TurboBoostDisable, "serverless", serverHandler.serverless)
	if !serverHandler.serverCfg.TurboBoostDisable && !serverHandler.serverless {
		dlog.Server.Info("Creating turbo aggregate for MapReduce", "query", queryStr)
		turboAggregate, err := server.NewTurboAggregate(queryStr, defaultLogFormat)
		if err != nil {
			return m, nil, nil, err
		}
		m.turboAggregate = turboAggregate
		return m, nil, turboAggregate, nil
	}

	// Otherwise, create a regular Aggregate
	aggregate, err := server.NewAggregate(queryStr, defaultLogFormat)
	if err != nil {
		return m, nil, nil, err
	}
	m.aggregate = aggregate
	return m, aggregate, nil, nil
}

func (m *mapCommand) Start(ctx context.Context, aggregatedMessages chan<- string) {
	if m.turboAggregate != nil {
		m.turboAggregate.Start(ctx, aggregatedMessages)
	} else {
		m.aggregate.Start(ctx, aggregatedMessages)
	}
}
