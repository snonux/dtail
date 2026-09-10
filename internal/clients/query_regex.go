package clients

import (
	"fmt"

	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/mapr"
)

func maprRegexForQuery(query *mapr.Query) string {
	if query == nil {
		return "."
	}

	switch query.Table {
	case "", ".":
		return "."
	case "*":
		return "\\|MAPREDUCE:\\|"
	default:
		return fmt.Sprintf("\\|MAPREDUCE:%s\\|", query.Table)
	}
}

func maprRegexFromQueryString(queryStr string) (*mapr.Query, string, error) {
	query, err := mapr.NewQuery(queryStr, dlog.Client)
	if err != nil {
		return nil, "", err
	}
	return query, maprRegexForQuery(query), nil
}
