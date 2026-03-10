package server

import "github.com/mimecast/dtail/internal/mapr"

const defaultLogFormat = "default"

func resolveParserName(query *mapr.Query, configuredLogFormat string) string {
	if query.LogFormat != "" {
		return query.LogFormat
	}
	if query.Table == "" {
		return "generic"
	}
	if configuredLogFormat == "" {
		return defaultLogFormat
	}
	return configuredLogFormat
}
