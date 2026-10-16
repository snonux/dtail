package clients

import (
	"errors"
	"strings"
)

// interactiveArgTokenizer splits the flags of an interactive `:reload` line
// into argument tokens with POSIX-shell-like quoting, so a value means what it
// would mean when typed on the command line:
//
//   - Outside quotes, a backslash makes the next rune literal (`a\ b` is one
//     token "a b", `\d` is "d"), and spaces and tabs separate tokens.
//   - Single quotes keep everything up to the closing quote literally,
//     backslashes included (`'\d+'` is `\d+`). This is the quoting to use for
//     regular expressions.
//   - Double quotes keep backslashes literally, except that `\\` and `\"`
//     stand for a backslash and a double quote (`"a\.b"` is `a\.b`).
//   - Quoted parts and unquoted parts join into one token when adjacent
//     (`--files=/a' 'b` is "--files=/a b"), and an empty quoted string (a
//     pair of single or of double quotes) is an empty argument, so that
//     `--regex ""` sets the regex to "". That is why the tokenizer tracks
//     whether a token was started separately from its length.
//
// There is no variable, command or glob expansion; `$` and `*` are literal.
type interactiveArgTokenizer struct {
	tokens  []string
	current strings.Builder
	// started is true once the current token began, even when it is still
	// empty because it consists of an empty quoted string only.
	started bool
	// quote is the quote rune of the quoted part being read, or 0 outside quotes.
	quote rune
	// escaped is true when the previous rune was an escaping backslash,
	// outside quotes or inside double quotes.
	escaped bool
}

// splitInteractiveArgs tokenizes the flags of an interactive `:reload` line,
// see interactiveArgTokenizer for the quoting rules. It fails on an
// unterminated quoted string or a trailing backslash.
func splitInteractiveArgs(raw string) ([]string, error) {
	var t interactiveArgTokenizer
	for _, r := range raw {
		t.consume(r)
	}
	return t.finish()
}

func (t *interactiveArgTokenizer) consume(r rune) {
	switch {
	case t.escaped:
		t.consumeEscaped(r)
	case t.quote == '\'':
		t.consumeSingleQuoted(r)
	case t.quote == '"':
		t.consumeDoubleQuoted(r)
	default:
		t.consumeUnquoted(r)
	}
}

// consumeEscaped handles the rune after an escaping backslash.
func (t *interactiveArgTokenizer) consumeEscaped(r rune) {
	t.escaped = false
	// Inside double quotes a backslash escapes only a backslash or a double
	// quote; before any other rune both are kept, as in a POSIX shell.
	if t.quote == '"' && r != '\\' && r != '"' {
		t.current.WriteRune('\\')
	}
	t.current.WriteRune(r)
}

func (t *interactiveArgTokenizer) consumeSingleQuoted(r rune) {
	if r == '\'' {
		t.quote = 0
		return
	}
	t.current.WriteRune(r)
}

func (t *interactiveArgTokenizer) consumeDoubleQuoted(r rune) {
	switch r {
	case '"':
		t.quote = 0
	case '\\':
		t.escaped = true
	default:
		t.current.WriteRune(r)
	}
}

func (t *interactiveArgTokenizer) consumeUnquoted(r rune) {
	switch r {
	case ' ', '\t':
		t.flush()
		return
	case '\'', '"':
		t.quote = r
	case '\\':
		t.escaped = true
	default:
		t.current.WriteRune(r)
	}
	// Opening a quote or an escape starts a token as well, so that `''` and
	// `""` yield an empty argument instead of nothing.
	t.started = true
}

// flush ends the current token, if one was started, empty or not.
func (t *interactiveArgTokenizer) flush() {
	if !t.started {
		return
	}
	t.tokens = append(t.tokens, t.current.String())
	t.current.Reset()
	t.started = false
}

func (t *interactiveArgTokenizer) finish() ([]string, error) {
	// An open quote is reported first: a backslash right before the end of
	// an open double-quoted string is part of that unterminated string.
	if t.quote != 0 {
		return nil, errors.New("unterminated quoted string")
	}
	if t.escaped {
		return nil, errors.New("unterminated escape sequence")
	}
	t.flush()
	return t.tokens, nil
}
