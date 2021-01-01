package logger

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mimecast/dtail/internal/color"
	"github.com/mimecast/dtail/internal/config"
)

const (
	clientStr string = "CLIENT"
	serverStr string = "SERVER"
	infoStr   string = "INFO"
	warnStr   string = "WARN"
	errorStr  string = "ERROR"
	fatalStr  string = "FATAL"
	debugStr  string = "DEBUG"
	traceStr  string = "TRACE"
)

// Mode specifies the configured logging mode(s)
var Mode Modes

// Strategy is the current log strattegy used.
var strategy Strategy

// Output to stdout (not log file)
var term termout

// The output files. We can have one per remote server.
var outfiles map[string]outfile

// Current hostname.
var hostname string

// Start logging.
func Start(ctx context.Context, mode Modes) {
	Mode = mode

	switch {
	case Mode.Nothing:
		return
	case Mode.Quiet:
		Mode.Trace = false
		Mode.Debug = false
	case Mode.Trace:
		Mode.Debug = true
	default:
	}

	strategy := logStrategy()

	switch strategy {
	case DailyStrategy:
		_, err := os.Stat(config.Common.LogDir)
		Mode.logToFile = !os.IsNotExist(err)
		Mode.logToStdout = !Mode.Server || Mode.Debug || Mode.Trace || Mode.Quiet
	case StdoutStrategy:
		fallthrough
	default:
		Mode.logToFile = !Mode.Server
		Mode.logToStdout = true
	}

	fqdn, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	s := strings.Split(fqdn, ".")
	hostname = s[0]

	// Setup logrotation
	logRotation(ctx)

	if Mode.logToStdout {
		term = newTermout(runtime.NumCPU() * 100)
		go term.start()
	}

	// Shutdown logging.
	go func() {
		<-ctx.Done()
		// TODO: Sync outfiles map access
		for _, outfile := range outfiles {
			outfile.stop()
		}
		if Mode.logToStdout {
			term.stop()
		}
	}()
}

func logRotation(ctx context.Context) {
	rotateCh := make(chan os.Signal, 1)
	signal.Notify(rotateCh, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-rotateCh:
				// TODO: Sync map access
				for _, outfile := range outfiles {
					outfile.rotate()
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Write log line to buffer and/or log file.
func write(what, severity, message, outName string) {
	if Mode.logToStdout {
		line := fmt.Sprintf("%s|%s|%s|%s\n", what, hostname, severity, message)

		if color.Colored {
			line = color.Colorfy(line)
		}

		term.bufCh <- line
	}

	if Mode.logToFile {
		// TODO: sync map access
		outfile, ok := outfiles[outName]
		if !ok {
			outfile = newOutfile(outName, 100)
			outfiles[outName] = outfile
			go outfile.start()
		}

		t := time.Now()
		timeStr := t.Format("20060102-150405")
		outfile.bufCh <- buf{
			time:    t,
			message: fmt.Sprintf("%s|%s|%s|%s\n", severity, timeStr, what, message),
		}
	}
}

// Generig log message.
func log(what string, severity string, args []interface{}) string {
	if Mode.Nothing {
		return ""
	}

	messages := []string{}

	for _, arg := range args {
		switch v := arg.(type) {
		case string:
			messages = append(messages, v)
		case int:
			messages = append(messages, fmt.Sprintf("%d", v))
		case error:
			messages = append(messages, v.Error())
		default:
			messages = append(messages, fmt.Sprintf("%v", v))
		}
	}

	message := strings.Join(messages, "|")
	write(what, severity, message, "")

	return fmt.Sprintf("%s|%s", severity, message)
}

// Raw message logging.
func Raw(message string) {
	if Mode.Nothing {
		return
	}

	if Mode.logToFile {
		fileLogBufCh <- buf{time.Now(), message}
	}

	if Mode.logToStdout {
		if color.Colored {
			message = color.Colorfy(message)
		}
		stdoutBufCh <- message
	}
}

// Flush all outstanding lines.
func Flush() {
	for {
		select {
		case message := <-stdoutBufCh:
			stdoutWriter.WriteString(message)
		default:
			stdoutWriter.Flush()
			return
		}
	}
}

func writeToStdout(ctx context.Context) {
	for {
		select {
		case message := <-stdoutBufCh:
			stdoutWriter.WriteString(message)
		case <-time.After(time.Millisecond * 100):
			stdoutWriter.Flush()
		case <-pauseCh:
		PAUSE:
			for {
				select {
				case <-stdoutBufCh:
				case <-resumeCh:
					break PAUSE
				case <-ctx.Done():
					return
				}
			}
		case <-ctx.Done():
			Flush()
			return
		}
	}
}

// Pause logging.
func Pause() {
	if Mode.logToStdout {
		pauser.pauseCh <- struct{}{}
	}
	if Mode.logToFile {
		for _, outfile := range outfiles {
			outfile.pause()
		}
	}
}

// Resume logging (after pausing).
func Resume() {
	if Mode.logToStdout {
		resumeCh <- struct{}{}
	}
	if Mode.logToFile {
		for _, outfile := range outfiles {
			outfile.resume()
		}
	}
}

// Info message logging.
func Info(args ...interface{}) string {
	if Mode.Server {
		return log(serverStr, infoStr, args)
	}

	return log(clientStr, infoStr, args)
}

// Warn message logging.
func Warn(args ...interface{}) string {
	if !Mode.Quiet {
		if Mode.Server {
			return log(serverStr, warnStr, args)
		}
		return log(clientStr, warnStr, args)
	}

	return ""
}

// Error message logging.
func Error(args ...interface{}) string {
	if Mode.Server {
		return log(serverStr, errorStr, args)
	}

	return log(clientStr, errorStr, args)
}

// Fatal message logging.
func Fatal(args ...interface{}) string {
	if Mode.Server {
		return log(serverStr, fatalStr, args)
	}

	return log(clientStr, fatalStr, args)
}

// FatalExit logs an error and exists the process.
func FatalExit(args ...interface{}) {
	what := clientStr
	if Mode.Server {
		what = serverStr
	}
	log(what, fatalStr, args)

	time.Sleep(time.Second)

	closeWriter()
	os.Exit(3)
}

// Debug message logging.
func Debug(args ...interface{}) string {
	if Mode.Debug {
		if Mode.Server {
			return log(serverStr, debugStr, args)
		}
		return log(clientStr, debugStr, args)
	}

	return ""
}

// Trace message logging.
func Trace(args ...interface{}) string {
	if Mode.Trace {
		if Mode.Server {
			return log(serverStr, traceStr, args)
		}
		return log(clientStr, traceStr, args)
	}

	return ""
}
