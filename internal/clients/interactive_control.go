package clients

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/clients/connectors"
	"github.com/mimecast/dtail/internal/config"
	"github.com/mimecast/dtail/internal/io/dlog"
	"github.com/mimecast/dtail/internal/omode"
)

const interactiveControlTimeout = 2 * time.Second

type interactiveCommand struct {
	spec SessionSpec
	kind string
	next config.Args
}

type interactiveReloadState struct {
	conn connectors.Connector
	spec SessionSpec
}

func (c *baseClient) startInteractiveControl(ctx context.Context, statsCh <-chan string) int {
	controlTTY, err := os.OpenFile(c.Args.ControlTTYPath, os.O_RDWR, 0)
	if err != nil {
		dlog.Client.Error("Unable to open interactive query control TTY", c.Args.ControlTTYPath, err)
		return 1
	}
	defer controlTTY.Close()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	statusCh := make(chan int, 1)
	go func() {
		statusCh <- c.runConnections(runCtx, statsCh)
	}()

	controlErrCh := make(chan error, 1)
	go func() {
		controlErrCh <- c.runInteractiveControl(runCtx, cancel, controlTTY)
	}()

	select {
	case status := <-statusCh:
		cancel()
		<-controlErrCh
		return status
	case err := <-controlErrCh:
		if err != nil {
			dlog.Client.Warn("Interactive query control stopped", err)
		}
		cancel()
		return <-statusCh
	}
}

func (c *baseClient) runInteractiveControl(ctx context.Context, cancel context.CancelFunc, tty *os.File) error {
	if _, err := fmt.Fprintf(tty,
		"Interactive query control enabled. Commands: :reload <flags>, :show, :help, :quit\n"); err != nil {
		return err
	}

	reader := bufio.NewScanner(tty)
	reader.Buffer(make([]byte, 0, 1024), 1024*1024)

	go func() {
		<-ctx.Done()
		_ = tty.Close()
	}()

	for reader.Scan() {
		line := strings.TrimSpace(reader.Text())
		if line == "" {
			continue
		}

		command, err := parseInteractiveCommand(c.Args, line)
		if err != nil {
			if writeErr := writeControlLine(tty, "interactive query error: "+err.Error()); writeErr != nil {
				return writeErr
			}
			continue
		}

		switch command.kind {
		case "help":
			if err := c.writeInteractiveHelp(tty); err != nil {
				return err
			}
		case "show":
			if err := c.writeInteractiveState(tty); err != nil {
				return err
			}
		case "quit":
			if err := writeControlLine(tty, "quitting interactive session"); err != nil {
				return err
			}
			cancel()
			return nil
		case "reload":
			if err := c.applyInteractiveReload(command.next, command.spec); err != nil {
				if writeErr := writeControlLine(tty, "reload failed: "+err.Error()); writeErr != nil {
					return writeErr
				}
				continue
			}
			if err := writeControlLine(tty, "reload applied successfully"); err != nil {
				return err
			}
		default:
			if err := writeControlLine(tty, "unsupported command"); err != nil {
				return err
			}
		}
	}

	if err := reader.Err(); err != nil && ctx.Err() == nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

func (c *baseClient) applyInteractiveReload(nextArgs config.Args, nextSpec SessionSpec) error {
	if len(c.connections) == 0 {
		return errors.New("no active connections")
	}
	prevArgs := c.Args
	prevSpec := c.sessionSpec

	var unsupported []string
	for _, conn := range c.connections {
		if !conn.SupportsQueryUpdates(interactiveControlTimeout) {
			unsupported = append(unsupported, conn.Server())
		}
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("%w: %s", connectors.ErrSessionUnsupported, strings.Join(unsupported, ", "))
	}

	applied, generation, err := c.applyInteractiveReloadConnections(nextSpec)
	if err != nil {
		return c.rollbackInteractiveReload(applied, prevArgs, prevSpec, err)
	}

	if committer, ok := c.maker.(sessionCommitter); ok {
		if err := committer.commitSessionSpec(nextSpec, generation); err != nil {
			return c.rollbackInteractiveReload(applied, prevArgs, prevSpec,
				fmt.Errorf("commit session state: %w", err))
		}
	}

	c.Args = nextArgs
	c.sessionSpec = nextSpec
	return nil
}

func (c *baseClient) applyInteractiveReloadConnections(nextSpec SessionSpec) ([]interactiveReloadState, uint64, error) {
	var generation uint64
	applied := make([]interactiveReloadState, 0, len(c.connections))
	for _, conn := range c.connections {
		prevSpec, _, _ := conn.CommittedSession()
		if err := conn.ApplySessionSpec(nextSpec, interactiveControlTimeout); err != nil {
			if shouldRollbackFailedReload(err) {
				applied = append(applied, interactiveReloadState{
					conn: conn,
					spec: prevSpec,
				})
			}
			return applied, 0, fmt.Errorf("%s: %w", conn.Server(), err)
		}
		applied = append(applied, interactiveReloadState{
			conn: conn,
			spec: prevSpec,
		})

		_, committedGeneration, ok := conn.CommittedSession()
		if !ok || committedGeneration == 0 {
			return applied, 0, fmt.Errorf("%s: missing committed session generation", conn.Server())
		}
		if generation == 0 {
			generation = committedGeneration
			continue
		}
		if generation != committedGeneration {
			return applied, 0, fmt.Errorf("mismatched committed generations: got %d and %d", generation, committedGeneration)
		}
	}
	return applied, generation, nil
}

func shouldRollbackFailedReload(err error) bool {
	return errors.Is(err, connectors.ErrSessionAckTimeout) ||
		errors.Is(err, connectors.ErrUnexpectedSessionAck)
}

func (c *baseClient) rollbackInteractiveReload(applied []interactiveReloadState, prevArgs config.Args, prevSpec SessionSpec, err error) error {
	rollbackErr := c.rollbackInteractiveReloadConnections(applied)
	c.Args = prevArgs
	c.sessionSpec = prevSpec
	if rollbackErr != nil {
		return errors.Join(err, rollbackErr)
	}
	return err
}

func (*baseClient) rollbackInteractiveReloadConnections(applied []interactiveReloadState) error {
	var rollbackErr error
	for i := len(applied) - 1; i >= 0; i-- {
		if err := applied[i].conn.ApplySessionSpec(applied[i].spec, interactiveControlTimeout); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("%s: rollback session spec: %w", applied[i].conn.Server(), err))
		}
	}
	return rollbackErr
}

func (c *baseClient) writeInteractiveHelp(writer io.Writer) error {
	return writeControlLine(writer,
		"Commands: :reload <flags>, :show, :help, :quit. Use quotes around multi-word values such as --query \"select count(status) from stats\".")
}

func (c *baseClient) writeInteractiveState(writer io.Writer) error {
	spec := c.sessionSpec
	ready := 0
	for _, conn := range c.connections {
		if conn.SupportsQueryUpdates(0) {
			ready++
		}
	}

	line := fmt.Sprintf(
		"mode=%s files=%s query=%q regex=%q options=%q timeout=%d capable=%d/%d",
		c.Args.Mode,
		strings.Join(spec.Files, ","),
		spec.Query,
		spec.Regex,
		spec.Options,
		spec.Timeout,
		ready,
		len(c.connections),
	)
	return writeControlLine(writer, line)
}

func parseInteractiveCommand(current config.Args, line string) (interactiveCommand, error) {
	line = strings.TrimSpace(line)

	switch {
	case line == ":help":
		return interactiveCommand{kind: "help"}, nil
	case line == ":show":
		return interactiveCommand{kind: "show"}, nil
	case line == ":quit":
		return interactiveCommand{kind: "quit"}, nil
	case strings.HasPrefix(line, ":reload"):
		remainder := strings.TrimSpace(strings.TrimPrefix(line, ":reload"))
		if remainder == "" {
			return interactiveCommand{}, errors.New("reload requires flags to change")
		}
		tokens, err := splitInteractiveArgs(remainder)
		if err != nil {
			return interactiveCommand{}, err
		}
		nextArgs, err := parseInteractiveReloadArgs(current, tokens)
		if err != nil {
			return interactiveCommand{}, err
		}
		nextSpec, err := buildInteractiveSessionSpec(nextArgs)
		if err != nil {
			return interactiveCommand{}, err
		}
		return interactiveCommand{
			kind: "reload",
			next: nextArgs,
			spec: nextSpec,
		}, nil
	default:
		return interactiveCommand{}, fmt.Errorf("unknown command %q", line)
	}
}

func parseInteractiveReloadArgs(current config.Args, tokens []string) (config.Args, error) {
	next := current
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&next.What, "files", current.What, "File(s) to read")
	fs.BoolVar(&next.Plain, "plain", current.Plain, "Plain output mode")
	fs.BoolVar(&next.Quiet, "quiet", current.Quiet, "Quiet output mode")
	fs.IntVar(&next.Timeout, "timeout", current.Timeout, "Max time dtail server will collect data until disconnection")

	switch {
	case isInteractiveQueryMode(current):
		fs.StringVar(&next.QueryStr, "query", current.QueryStr, "Map reduce query")
	case current.Mode == omode.GrepClient || current.Mode == omode.TailClient:
		var grep string
		fs.StringVar(&next.RegexStr, "regex", current.RegexStr, "Regular expression")
		fs.StringVar(&grep, "grep", "", "Alias for -regex")
		fs.BoolVar(&next.RegexInvert, "invert", current.RegexInvert, "Invert regex")
		fs.IntVar(&next.LContext.BeforeContext, "before", current.LContext.BeforeContext, "Leading context lines")
		fs.IntVar(&next.LContext.AfterContext, "after", current.LContext.AfterContext, "Trailing context lines")
		fs.IntVar(&next.LContext.MaxCount, "max", current.LContext.MaxCount, "Maximum number of matches")
		if err := fs.Parse(tokens); err != nil {
			return current, err
		}
		if grep != "" {
			next.RegexStr = grep
		}
		if len(fs.Args()) > 0 {
			return current, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
		}
		return next, nil
	case current.Mode == omode.CatClient:
	default:
		return current, fmt.Errorf("interactive reload is unsupported for mode %s", current.Mode)
	}

	if err := fs.Parse(tokens); err != nil {
		return current, err
	}
	if len(fs.Args()) > 0 {
		return current, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return next, nil
}

func buildInteractiveSessionSpec(args config.Args) (SessionSpec, error) {
	normalizedArgs, err := normalizeInteractiveArgs(args)
	if err != nil {
		return SessionSpec{}, err
	}

	spec := NewSessionSpec(normalizedArgs)
	if _, err := spec.Commands(); err != nil {
		return SessionSpec{}, err
	}
	return spec, nil
}

func normalizeInteractiveArgs(args config.Args) (config.Args, error) {
	if !isInteractiveQueryMode(args) {
		return args, nil
	}

	_, regexValue, err := maprRegexFromQueryString(args.QueryStr)
	if err != nil {
		return args, err
	}
	args.RegexStr = regexValue
	return args, nil
}

func isInteractiveQueryMode(args config.Args) bool {
	return strings.TrimSpace(args.QueryStr) != "" &&
		(args.Mode == omode.MapClient || args.Mode == omode.TailClient)
}

func splitInteractiveArgs(raw string) ([]string, error) {
	var (
		tokens  []string
		current strings.Builder
		inQuote rune
		escaped bool
	)

	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case inQuote != 0:
			if r == inQuote {
				inQuote = 0
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			inQuote = r
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
		}
	}

	if escaped {
		return nil, errors.New("unterminated escape sequence")
	}
	if inQuote != 0 {
		return nil, errors.New("unterminated quoted string")
	}

	flush()
	return tokens, nil
}

func writeControlLine(writer io.Writer, message string) error {
	_, err := fmt.Fprintf(writer, "%s\n", message)
	return err
}
