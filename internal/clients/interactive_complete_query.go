package clients

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/mapr"
	"github.com/mimecast/dtail/internal/mapr/funcs"
	"github.com/mimecast/dtail/internal/mapr/logformat"
)

// queryCatalogHint is listed with the candidates of a query word that would
// be completed from the field catalog while that holds nothing yet.
const queryCatalogHint = "(no field data from the servers yet: only the query language completes)"

// queryCompletion is what completing a word of a --query value found: the
// candidates for the word (in the query's own syntax, not yet escaped for
// the shell quote) and an optional hint line.
type queryCompletion struct {
	word       string
	candidates []string
	// kinds is the kind of every candidate, for the listing's style.
	kinds map[string]candidateKind
	hint  string
}

// completeQueryWord completes the word at the end of query, a --query value
// typed up to the cursor, from the query language (mapr.AnalyzePartial and
// the vocabulary tables) and the field catalog. It returns no candidates
// where nothing can be offered (a number, an outfile path).
func completeQueryWord(query string, catalog catalogSnapshot) queryCompletion {
	partial := mapr.AnalyzePartial(query)
	source := newQueryCandidateSource(partial, catalog)
	var candidates []string
	kinds := map[string]candidateKind{}
	for _, kind := range queryCandidateKinds {
		if partial.Expect.Has(kind.expect) {
			found := kind.candidates(source)
			for _, candidate := range found {
				if _, seen := kinds[candidate]; !seen {
					kinds[candidate] = kind.kind
				}
			}
			candidates = append(candidates, found...)
		}
	}
	// Tables, fields, $variables and values of the catalog are log content:
	// a candidate is printed in the listing and inserted into the edit line,
	// so one holding a control or format character (an escape sequence, a
	// bidirectional override) is dropped here, for every kind at once, as
	// the path completion drops such names (readableFilesName).
	candidates = slices.DeleteFunc(candidates, hasHiddenRune)
	candidates = matchingQueryWord(dedupe(candidates), partial.Word)
	result := queryCompletion{word: partial.Word, candidates: candidates, kinds: kinds}
	if catalog.empty() && partial.Expect.Has(catalogExpectations) {
		result.hint = queryCatalogHint
	}
	return result
}

// catalogExpectations are the expected kinds whose candidates come from the
// field catalog.
const catalogExpectations = mapr.ExpectField | mapr.ExpectAggregationField |
	mapr.ExpectTable | mapr.ExpectValue

// queryCandidateKinds maps every expected kind of word to the function that
// lists its candidates, in the order candidates are offered, and to the
// kind they are styled as in the listing (a field starting with $ is styled
// as a variable, see completion.kindOf).
var queryCandidateKinds = []struct {
	expect     mapr.Expect
	candidates func(queryCandidateSource) []string
	kind       candidateKind
}{
	{mapr.ExpectClauseKeyword, queryKeywordCandidates, candidateKeyword},
	{mapr.ExpectAggregation, queryAggregationCandidates, candidateFunction},
	{mapr.ExpectAggregationField, queryAggregationFieldCandidates, candidateField},
	{mapr.ExpectField, queryFieldCandidates, candidateField},
	{mapr.ExpectSetVariable, queryVariableCandidates, candidateVariable},
	{mapr.ExpectSetFunction, queryFunctionCandidates, candidateFunction},
	{mapr.ExpectWhereOperator, queryOperatorCandidates, candidateOperator},
	{mapr.ExpectValue, queryValueCandidates, candidateValue},
	{mapr.ExpectAnd, func(queryCandidateSource) []string { return []string{"and "} }, candidateKeyword},
	{mapr.ExpectAssign, func(queryCandidateSource) []string { return []string{"= "} }, candidateOperator},
	{mapr.ExpectBy, func(queryCandidateSource) []string { return []string{"by "} }, candidateKeyword},
	{mapr.ExpectAppend, func(queryCandidateSource) []string { return []string{"append "} }, candidateKeyword},
	{mapr.ExpectTable, queryTableCandidates, candidateTable},
	{mapr.ExpectLogFormat, queryLogFormatCandidates, candidateKeyword},
	{mapr.ExpectSelectItem, querySelectItemCandidates, candidateField},
}

// queryCandidateSource is what the candidate functions draw from: the
// analysis of the query and the log formats whose catalog data applies.
type queryCandidateSource struct {
	partial mapr.Partial
	// logFormats are the formats whose fields and $variables are offered:
	// the query's effective log format and, while neither a table nor a
	// log format is typed (the query is still incomplete; without a table
	// the generic format would apply), also the servers' default format,
	// which the query uses once it names a table.
	logFormats []string
	// tableFormat is the format whose tables are offered after from: the
	// typed log format, else the servers' default one.
	tableFormat string
	catalog     catalogSnapshot
}

func newQueryCandidateSource(partial mapr.Partial, catalog catalogSnapshot) queryCandidateSource {
	defaultFormat := catalog.DefaultLogFormat
	if defaultFormat == "" {
		defaultFormat = "default"
	}
	source := queryCandidateSource{partial: partial, catalog: catalog, tableFormat: defaultFormat,
		logFormats: []string{partial.EffectiveLogFormat(defaultFormat)}}
	if partial.LogFormat != "" {
		source.tableFormat = partial.LogFormat
	}
	if partial.Table == "" && partial.LogFormat == "" && source.logFormats[0] != defaultFormat {
		source.logFormats = append(source.logFormats, defaultFormat)
	}
	return source
}

// closing returns what ends a word of the source: the closing double quote
// or backtick the word is in, otherwise sep (a blank or a parenthesis).
func (s queryCandidateSource) closing(sep string) string {
	if quote := s.quote(); quote != "" {
		return quote
	}
	return sep
}

// quote returns the double quote or backtick the word is in, if any.
func (s queryCandidateSource) quote() string {
	switch {
	case s.partial.InQuote:
		return `"`
	case s.partial.InBacktick:
		return "`"
	}
	return ""
}

func queryKeywordCandidates(queryCandidateSource) []string {
	return withSuffix(mapr.Keywords(), " ")
}

func queryAggregationCandidates(queryCandidateSource) []string {
	return withSuffix(mapr.AggregationNames(), "(")
}

func queryFunctionCandidates(queryCandidateSource) []string {
	return withSuffix(funcs.Names(), "(")
}

func queryOperatorCandidates(queryCandidateSource) []string {
	var names []string
	for _, op := range mapr.WhereOperators() {
		names = append(names, op.Name)
	}
	return withSuffix(names, " ")
}

// queryLogFormatCandidates lists the log format names, closing the double
// quote the word may be in (logformat "csv" is the format csv).
func queryLogFormatCandidates(s queryCandidateSource) []string {
	return withSuffix(logformat.Names(), s.closing("")+" ")
}

// querySelectItemCandidates lists the select items for order by, written
// so that the parser reads each as that item (querySelectItemWords).
func querySelectItemCandidates(s queryCandidateSource) []string {
	return querySelectItemWords(s.partial.SelectItems, s.quote())
}

// queryFieldCandidates lists the fields of the query's table (of every
// table while none is typed) and the $variables of its log format. Inside a
// query string quote, AnalyzePartial expects a field only where a quoted
// token still is one (select, group by), not where it is a string literal
// (a where or set condition); there the fields are written for the quote
// and closed (quotedQueryWords).
func queryFieldCandidates(s queryCandidateSource) []string {
	names := append(s.fieldNames(), s.variableNames()...)
	if s.partial.InQuote {
		return withSuffix(quotedQueryWords(names, true), " ")
	}
	if s.inCall() {
		// A function argument, e.g. md5sum(: written plain and closed.
		return withSuffix(queryFieldWords(names, true), ")")
	}
	return withSuffix(queryFieldWords(names, s.partial.InBacktick), s.closing(" "))
}

// queryAggregationFieldCandidates lists the fields inside AGG(, closed by
// the parenthesis. They are written without backticks even when named like
// a keyword: the parser strips backticks only around a whole token, so
// count(`from`) would count a field named `from` with its backticks, while
// count(from) is no keyword token and counts the field from.
func queryAggregationFieldCandidates(s queryCandidateSource) []string {
	if s.partial.InQuote {
		return nil
	}
	names := append(s.fieldNames(), s.variableNames()...)
	return withSuffix(queryFieldWords(names, true), s.closing(")"))
}

// inCall reports whether the word is the argument of a call written right
// after its opening parenthesis (a set function such as md5sum(), where a
// field is written like inside AGG(.
func (s queryCandidateSource) inCall() bool {
	p := s.partial
	return !p.InBacktick && p.WordStart > 0 && p.Query[p.WordStart-1] == '('
}

func queryVariableCandidates(s queryCandidateSource) []string {
	return withSuffix(queryFieldWords(s.variableNames(), false), " ")
}

// queryTableCandidates lists the tables the servers found for the query's
// log format (and for every format when it has none).
func queryTableCandidates(s queryCandidateSource) []string {
	var tables []string
	for table := range s.catalog.Formats[s.tableFormat].Tables {
		if table != "" {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	if s.partial.InQuote {
		// from "STATS" is the table STATS: written as is and closed.
		return withSuffix(quotedQueryWords(tables, false), " ")
	}
	// Bare (a keyword in backticks) or, inside backticks, as is and closed.
	return withSuffix(queryFieldWords(tables, s.partial.InBacktick), s.closing(" "))
}

// queryValueCandidates lists the sample values of the where condition's
// field: double-quoted after a string operator (quotedValueWord; values
// containing a double quote, which the query language can not quote, or a
// line break are left out, as completeQueryWord leaves out every value with
// another control or format character), plain decimal numbers after a
// numeric one. A sample of the probe's
// full value length may have been cut (logformat.ProbeMaxValueBytes), so it
// is only offered for operators that compare a prefix or a part.
func queryValueCandidates(s queryCandidateSource) []string {
	op := s.partial.ValueOperator
	if op.Name == "" {
		return nil
	}
	var values []string
	for _, value := range s.samples(s.partial.ValueField) {
		if len(value) >= logformat.ProbeMaxValueBytes && !partialValueOperators[op.Name] {
			continue
		}
		switch {
		case op.Kind == mapr.NumericOperator:
			if decimalNumber.MatchString(value) && !s.partial.InQuote {
				values = append(values, value+" ")
			}
		case !strings.ContainsAny(value, "\n\r"):
			values = append(values, quotedValueWord(value, s.partial.InQuote)...)
		}
	}
	return values
}

// quotedValueWord writes a sample value as the double-quoted string of a
// where condition (only the rest and the closing quote when the word is in
// the quote already): the parser strips a pair of backticks around a quoted
// token too, so a value wrapped in backticks gets another pair
// (quotedQueryWord). A value with a double quote, which the query language
// can not quote, gives no word.
func quotedValueWord(value string, inQuote bool) []string {
	word, ok := quotedQueryWord(value, false)
	switch {
	case !ok:
		return nil
	case inQuote:
		return []string{word + " "}
	}
	return []string{`"` + word + " "}
}

// partialValueOperators compare a prefix or a part of a value, for which a
// sample cut at the probe's value length still works.
var partialValueOperators = map[string]bool{"contains": true, "ncontains": true,
	"lacks": true, "hasprefix": true, "nhasprefix": true}

// decimalNumber matches the plain decimal numbers offered after a numeric
// operator (no NaN, Inf or hexadecimal forms, which strconv would accept).
var decimalNumber = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

// fieldNames returns the catalog fields of the query's table, or of every
// table of its log formats while no table is typed.
func (s queryCandidateSource) fieldNames() []string {
	var names []string
	for _, name := range s.logFormats {
		for table, fields := range s.catalog.Formats[name].Tables {
			if s.partial.Table != "" && table != s.partial.Table {
				continue
			}
			for field := range fields {
				names = append(names, field)
			}
		}
	}
	sort.Strings(names)
	return dedupe(names)
}

// variableNames returns the $variables of the query's log formats: the ones
// the servers saw and the ones the formats are known to fill.
func (s queryCandidateSource) variableNames() []string {
	var names []string
	for _, format := range s.logFormats {
		for name := range s.catalog.Formats[format].Variables {
			names = append(names, name)
		}
		if known, ok := logformat.KnownVariables(format); ok {
			names = append(names, known...)
		}
	}
	sort.Strings(names)
	return dedupe(names)
}

// samples returns the catalog's sample values of field (a field of the
// query's table or a $variable).
func (s queryCandidateSource) samples(field string) []string {
	var values []string
	for _, name := range s.logFormats {
		format := s.catalog.Formats[name]
		values = append(values, format.Variables[field]...)
		for table, fields := range format.Tables {
			if s.partial.Table == "" || table == s.partial.Table {
				values = append(values, fields[field]...)
			}
		}
	}
	return dedupe(values)
}

// empty reports whether no server answered yet.
func (c catalogSnapshot) empty() bool {
	return len(c.Servers) == 0
}

// matchingQueryWord keeps the candidates that start with word, ignoring case
// (keywords and operators ignore case in queries; for the other words the
// candidate's own case is what gets inserted). A double-quoted value, or a
// field in backticks, also matches a word typed without its opening quote
// or backtick, which it then replaces.
func matchingQueryWord(candidates []string, word string) []string {
	lowerWord := strings.ToLower(word)
	var out []string
	for _, candidate := range candidates {
		lower := strings.ToLower(candidate)
		if strings.HasPrefix(lower, lowerWord) ||
			(!strings.HasPrefix(word, `"`) && strings.HasPrefix(strings.TrimPrefix(lower, `"`), lowerWord)) ||
			(!strings.HasPrefix(word, "`") && strings.HasPrefix(strings.TrimPrefix(lower, "`"), lowerWord)) {
			out = append(out, candidate)
		}
	}
	return out
}

func withSuffix(names []string, suffix string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = name + suffix
	}
	return out
}

func dedupe(candidates []string) []string {
	seen := make(map[string]bool, len(candidates))
	out := candidates[:0:0]
	for _, candidate := range candidates {
		if !seen[candidate] {
			seen[candidate] = true
			out = append(out, candidate)
		}
	}
	return slices.Clip(out)
}

// queryFlagNames are the spellings of the --query flag a value belongs to.
var queryFlagNames = []string{"--query", "-query"}

// completeQueryValue completes the word at the cursor when it is inside the
// value of :reload --query (after "--query " in an open quote or escaped, or
// in "--query=..."), in a mode whose :reload accepts --query. It reports
// false elsewhere, also where the value has not started (then the current
// query is inserted as before). The catalog is only read here. Candidates
// are matched and their common prefix taken on the decoded text, and only
// the replacement is escaped for the open shell quote of the value, so the
// line still splits into the intended query; a word whose bytes were not
// all read in that quote (it spans a quote change) is left alone.
func completeQueryValue(line string, pos int, args config.Args, catalog func() catalogSnapshot) (completion, bool) {
	before, after := line[:pos], line[pos:]
	rest, ok := reloadArguments(before)
	if !ok || !slices.Contains(interactiveReloadFlags(args), "--query ") {
		return completion{}, false
	}
	parsed := splitInteractiveArgsPartial(rest)
	value, valueStart, ok := queryValueOf(parsed)
	if !ok {
		return completion{}, false
	}
	unchanged := completion{line: line, pos: pos}
	query := completeQueryWord(value, catalog())
	wordStart := valueStart + len(value) - len(query.word)
	if !parsed.suffixInQuote(wordStart) {
		return unchanged, true
	}
	rawStart := len(before) - len(rest) + parsed.rawOffset(wordStart, len(rest))
	replacement, listed := decodedReplacement(query.word, query.candidates)
	if replacement == "" {
		unchanged.candidates = listed
		unchanged.kinds = query.kinds
		unchanged.hint = query.hint
		return unchanged, true
	}
	newLine := before[:rawStart] + escapeForQuote(replacement, parsed.quote) + after
	newPos := len(newLine) - len(after)
	return completion{line: newLine, pos: newPos, changed: newLine != line, hint: query.hint}, true
}

// decodedReplacement returns what replaces word: the only candidate, or the
// common prefix of several (cut back to a whole UTF-8 character) when it is
// longer than word; otherwise "" and the candidates to list. All of it is
// decoded query text, escaped by the caller only after the prefix is taken,
// so that a prefix can never end inside an escape sequence.
func decodedReplacement(word string, candidates []string) (string, []string) {
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return candidates[0], nil
	}
	prefix := runePrefix(commonPrefix(candidates))
	if len(prefix) <= len(word) {
		return "", candidates
	}
	return prefix, nil
}

// runePrefix cuts s back to its longest prefix that ends at a UTF-8
// character boundary, so a common prefix of candidates sharing only the
// first byte of a multi-byte character inserts no broken byte.
func runePrefix(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// queryValueOf returns the --query value the cursor is in (decoded so far)
// and the offset in parsed.current where it starts (flagValueOf).
func queryValueOf(parsed partialArgs) (string, int, bool) {
	return flagValueOf(parsed, queryFlagNames)
}
