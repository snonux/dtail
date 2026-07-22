package server

import "github.com/mimecast/dtail/internal/mapr"

// resolveParserName determines which log format parser evaluates the query on
// the server side. The selection rule lives in mapr.Query.EffectiveLogFormat so
// that the client-side plan-time diagnostics target the exact same parser.
func resolveParserName(query *mapr.Query, configuredLogFormat string) string {
	return query.EffectiveLogFormat(configuredLogFormat)
}
