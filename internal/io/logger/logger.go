package logger

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
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

// The configured logging mode(s)
var mode Modes

// Strategy is the current log strattegy used.
var strategy Strategy

// Synchronise access to logging.
var mutex sync.Mutex

// File descriptor of log file when mode.logToFile enabled.
var fd *os.File

// File write buffer of log file when mode.logToFile enabled.
var writer *bufio.Writer

// File write buffer of stdout when mode.logToStdout enabled.
var stdoutWriter *bufio.Writer

// Current hostname.
var hostname string

// Used to detect change of day (create one log file per day0
var lastDateStr string

// Used to make logging non-blocking.
var fileLogBufCh chan buf
var stdoutBufCh chan string

// Stdout channel, required to pause output
var pauseCh chan struct{}
var resumeCh chan struct{}

// Tell the logger about logrotation
var rotateCh chan os.Signal

// Helper type to make logging non-blocking.
type buf struct {
	time    time.Time
	message string
}

// Start logging.
func Start(ctx context.Context, myMode Modes) {
	mode = myMode

	if mode.Nothing {
		return
	}

	if mode.Trace {
		mode.Debug = true
	}

	strategy := logStrategy()
	stdoutWriter = bufio.NewWriter(os.Stdout)

	switch strategy {
	case DailyStrategy:
		_, err := os.Stat(config.Common.LogDir)
		mode.logToFile = !os.IsNotExist(err)
		mode.logToStdout = !mode.Server || mode.Debug || mode.Trace
	case StdoutStrategy:
		fallthrough
	default:
		mode.logToFile = !mode.Server
		mode.logToStdout = true
	}

	fqdn, err := os.Hostname()
	if err != nil {
		panic(err)
	}
	s := strings.Split(fqdn, ".")
	hostname = s[0]

	pauseCh = make(chan struct{})
	resumeCh = make(chan struct{})

	// Setup logrotation
	rotateCh = make(chan os.Signal, 1)
	signal.Notify(rotateCh, syscall.SIGHUP)

	if mode.logToStdout {
		stdoutBufCh = make(chan string, runtime.NumCPU()*100)
		go writeToStdout(ctx)
	}

	if mode.logToFile {
		fileLogBufCh = make(chan buf, runtime.NumCPU()*100)
		go writeToFile(ctx)
	}
}

// Info message logging.
func Info(args ...interface{}) string {
	if mode.Server {
		return log(serverStr, infoStr, args)
	}

	return log(clientStr, infoStr, args)
}

// Warn message logging.
func Warn(args ...interface{}) string {
	if mode.Server {
		return log(serverStr, warnStr, args)
	}

	return log(clientStr, warnStr, args)
}

// Error message logging.
func Error(args ...interface{}) string {
	if mode.Server {
		return log(serverStr, errorStr, args)
	}

	return log(clientStr, errorStr, args)
}

// FatalExit logs an error and exists the process.
func FatalExit(args ...interface{}) {
	what := clientStr
	if mode.Server {
		what = serverStr
	}
	log(what, fatalStr, args)

	time.Sleep(time.Second)
	mutex.Lock()
	defer mutex.Unlock()

	closeWriter()
	os.Exit(3)
}

// Debug message logging.
func Debug(args ...interface{}) string {
	if mode.Debug {
		if mode.Server {
			return log(serverStr, debugStr, args)
		}
		return log(clientStr, debugStr, args)
	}

	return ""
}

// Trace message logging.
func Trace(args ...interface{}) string {
	if mode.Trace {
		if mode.Server {
			return log(serverStr, traceStr, args)
		}
		return log(clientStr, traceStr, args)
	}

	return ""
}

// Write log line to buffer and/or log file.
func write(what, severity, message string) {
	if mode.logToStdout {
		line := fmt.Sprintf("%s|%s|%s|%s\n", what, hostname, severity, message)

		if color.Colored {
			line = color.Colorfy(line)
		}

		stdoutBufCh <- line
	}

	if mode.logToFile {
		t := time.Now()
		timeStr := t.Format("20060102-150405")
		fileLogBufCh <- buf{
			time:    t,
			message: fmt.Sprintf("%s|%s|%s|%s\n", severity, timeStr, what, message),
		}
	}
}

// Generig log message.
func log(what string, severity string, args []interface{}) string {
	if mode.Nothing {
		return ""
	}
	if mode.Quiet && severity != errorStr && severity != fatalStr {
		return ""
	}

	messages := []string{severity}

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
	write(what, severity, message)

	return message
}

// Raw message logging.
func Raw(message string) {
	if mode.Nothing {
		return
	}

	if mode.logToFile {
		fileLogBufCh <- buf{time.Now(), message}
	}

	if mode.logToStdout {
		if color.Colored {
			message = color.Colorfy(message)
		}
		stdoutBufCh <- message
	}
}

// Close log writer (e.g. on change of day).
func closeWriter() {
	if writer != nil {
		writer.Flush()
		fd.Close()
	}
}

// Return the correct log file writer
func fileWriter(dateStr string) *bufio.Writer {
	if dateStr != lastDateStr {
		return updateFileWriter(dateStr)
	}

	// Check for log rotation signal
	select {
	case <-rotateCh:
		stdoutWriter.WriteString("Received signal for logrotation\n")
		return updateFileWriter(dateStr)
	default:
	}

	return writer
}

// Update log file writer
func updateFileWriter(dateStr string) *bufio.Writer {
	// Detected change of day. Close current writer and create a new one.
	mutex.Lock()
	defer mutex.Unlock()
	closeWriter()

	if _, err := os.Stat(config.Common.LogDir); os.IsNotExist(err) {
		if err = os.MkdirAll(config.Common.LogDir, 0755); err != nil {
			panic(err)
		}
	}

	logFile := fmt.Sprintf("%s/%s.log", config.Common.LogDir, dateStr)
	newFd, err := os.OpenFile(logFile, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}

	fd = newFd
	writer = bufio.NewWriterSize(fd, 1)
	lastDateStr = dateStr

	return writer
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

func writeToFile(ctx context.Context) {
	for {
		select {
		case buf := <-fileLogBufCh:
			dateStr := buf.time.Format("20060102")
			w := fileWriter(dateStr)
			w.WriteString(buf.message)
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
			return
		}
	}
}

// Pause logging.
func Pause() {
	if mode.logToStdout {
		pauseCh <- struct{}{}
	}
	if mode.logToFile {
		pauseCh <- struct{}{}
	}
}

// Resume logging (after pausing).
func Resume() {
	if mode.logToStdout {
		resumeCh <- struct{}{}
	}
	if mode.logToFile {
		resumeCh <- struct{}{}
	}
}
