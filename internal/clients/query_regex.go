package clients

import (
	"fmt"
	"io"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/logging"
	"github.com/mimecast/dtail/internal/mapr"
)

// ignoredInvertWarning tells the user that dtail dropped --invert because a
// mapreduce query was given (see applyQueryRegex).
const ignoredInvertWarning = "WARN: Ignoring --invert: a mapreduce query (--query) " +
	"selects the lines of its table itself"

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

// applyQueryRegex sets the line filter of a mapreduce query on args: the
// regex is derived from the query's table (maprRegexForQuery) and replaces any
// --regex, and RegexInvert is cleared. The filter only selects the lines the
// query aggregates, so inverting it has no sensible meaning: for a named table
// it would feed every other line into the aggregation, and for the tables ""
// and "." (every line) only the empty lines. dtail therefore accepts --invert
// together with --query and ignores it, with a warning (warnIgnoredInvert).
//
// The read commands built from a query session spec ignore RegexInvert
// anyway (session.Spec.queryCommands), so clearing it here is not needed for
// a current dserver. It is kept so that the client's own args and the
// regex_invert field of a SESSION START/UPDATE spec say what is done, and
// because an older dserver builds the read commands of such a spec itself
// and would invert the filter.
func applyQueryRegex(args *config.Args, query *mapr.Query) {
	args.RegexStr = maprRegexForQuery(query)
	args.RegexInvert = false
}

// warnIgnoredInvert writes ignoredInvertWarning to w when args asks to invert
// the regex of a mapreduce query, which applyQueryRegex ignores.
func warnIgnoredInvert(w io.Writer, args config.Args, logger logging.Logger) {
	if !args.RegexInvert {
		return
	}
	if _, err := fmt.Fprintln(w, ignoredInvertWarning); err != nil {
		logger.Debug("Unable to write ignored invert warning", err)
	}
}
