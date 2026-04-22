//go:build linux

package journal

import (
	"bytes"
	"context"

	"github.com/mimecast/dtail/internal/io/line"
	"github.com/mimecast/dtail/internal/io/pool"
	"github.com/mimecast/dtail/internal/lcontext"
	"github.com/mimecast/dtail/internal/regex"
)

type journalSink interface {
	Emit(context.Context, *bytes.Buffer, uint64, int, string) error
	Full() bool
}

type channelSink struct {
	lines chan<- *line.Line
	skip  bool
}

func (s channelSink) Emit(ctx context.Context, rawLine *bytes.Buffer, count uint64,
	transmittedPerc int, sourceID string) error {

	newLine := line.New(rawLine, count, transmittedPerc, sourceID)
	select {
	case s.lines <- newLine:
		return nil
	case <-ctx.Done():
		pool.RecycleBytesBuffer(rawLine)
		newLine.Recycle()
		return ctx.Err()
	}
}

func (s channelSink) Full() bool {
	return s.skip && len(s.lines) >= cap(s.lines)
}

type processorSink struct {
	processor line.Processor
}

func (s processorSink) Emit(_ context.Context, rawLine *bytes.Buffer, count uint64,
	_ int, sourceID string) error {

	err := s.processor.ProcessLine(rawLine, count, sourceID)
	if err != nil {
		pool.RecycleBytesBuffer(rawLine)
	}
	return err
}

func (s processorSink) Full() bool {
	return false
}

type journalFilter struct {
	ltx      lcontext.LContext
	sink     journalSink
	re       regex.Regex
	sourceID string
	stats    journalStats

	before    []bufferedLine
	after     int
	maxCount  int
	maxHit    int
	maxClosed bool
}

type bufferedLine struct {
	content *bytes.Buffer
	count   uint64
}

func newJournalFilter(ltx lcontext.LContext, sink journalSink, re regex.Regex,
	sourceID string) *journalFilter {

	return &journalFilter{
		ltx:      ltx,
		sink:     sink,
		re:       re,
		sourceID: sourceID,
		maxCount: ltx.MaxCount,
	}
}

func (f *journalFilter) Process(ctx context.Context, rawLine *bytes.Buffer) error {
	f.stats.updatePosition()
	if !f.ltx.Has() {
		return f.processWithoutContext(ctx, rawLine)
	}
	return f.processWithContext(ctx, rawLine)
}

func (f *journalFilter) Close() {
	for _, line := range f.before {
		pool.RecycleBytesBuffer(line.content)
	}
	f.before = nil
}

func (f *journalFilter) processWithoutContext(ctx context.Context, rawLine *bytes.Buffer) error {
	if !f.re.Match(rawLine.Bytes()) {
		f.stats.updateLineNotMatched()
		f.stats.updateLineNotTransmitted()
		pool.RecycleBytesBuffer(rawLine)
		return nil
	}

	f.stats.updateLineMatched()
	if f.sink.Full() {
		f.stats.updateLineNotTransmitted()
		pool.RecycleBytesBuffer(rawLine)
		return nil
	}
	f.stats.updateLineTransmitted()
	return f.sink.Emit(ctx, rawLine, f.stats.totalLineCount(), f.stats.transmittedPerc(), f.sourceID)
}

func (f *journalFilter) processWithContext(ctx context.Context, rawLine *bytes.Buffer) error {
	if !f.re.Match(rawLine.Bytes()) {
		return f.processContextMiss(ctx, rawLine)
	}

	f.stats.updateLineMatched()
	if f.maxClosed {
		pool.RecycleBytesBuffer(rawLine)
		return errStopReading
	}

	if err := f.emitBefore(ctx); err != nil {
		pool.RecycleBytesBuffer(rawLine)
		return err
	}
	f.stats.updateLineTransmitted()
	if err := f.sink.Emit(ctx, rawLine, f.stats.totalLineCount(), 100, f.sourceID); err != nil {
		return err
	}

	if f.maxCount > 0 {
		f.maxHit++
		if f.maxHit >= f.maxCount {
			if f.ltx.AfterContext == 0 {
				return errStopReading
			}
			f.maxClosed = true
		}
	}
	if f.ltx.AfterContext > 0 {
		f.after = f.ltx.AfterContext
	}
	return nil
}

func (f *journalFilter) processContextMiss(ctx context.Context, rawLine *bytes.Buffer) error {
	f.stats.updateLineNotMatched()
	if f.maxClosed && f.after == 0 {
		pool.RecycleBytesBuffer(rawLine)
		return errStopReading
	}
	if f.after > 0 {
		f.after--
		f.stats.updateLineTransmitted()
		err := f.sink.Emit(ctx, rawLine, f.stats.totalLineCount(), 100, f.sourceID)
		if err == nil && f.maxClosed && f.after == 0 {
			return errStopReading
		}
		return err
	}
	if f.ltx.BeforeContext > 0 {
		f.rememberBefore(rawLine)
		f.stats.updateLineNotTransmitted()
		return nil
	}

	f.stats.updateLineNotTransmitted()
	pool.RecycleBytesBuffer(rawLine)
	return nil
}

func (f *journalFilter) rememberBefore(rawLine *bytes.Buffer) {
	if len(f.before) >= f.ltx.BeforeContext {
		pool.RecycleBytesBuffer(f.before[0].content)
		copy(f.before, f.before[1:])
		f.before = f.before[:len(f.before)-1]
	}
	f.before = append(f.before, bufferedLine{
		content: rawLine,
		count:   f.stats.totalLineCount(),
	})
}

func (f *journalFilter) emitBefore(ctx context.Context) error {
	for i, line := range f.before {
		f.stats.updateLineTransmitted()
		if err := f.sink.Emit(ctx, line.content, line.count, 100, f.sourceID); err != nil {
			f.discardBeforeFrom(i + 1)
			return err
		}
	}
	f.before = f.before[:0]
	return nil
}

func (f *journalFilter) discardBeforeFrom(index int) {
	for _, line := range f.before[index:] {
		pool.RecycleBytesBuffer(line.content)
	}
	f.before = f.before[:0]
}

type journalStats struct {
	pos           int
	lineCount     uint64
	matched       [100]bool
	matchCount    uint64
	transmitted   [100]bool
	transmitCount int
}

func (s *journalStats) totalLineCount() uint64 {
	return s.lineCount
}

func (s *journalStats) transmittedPerc() int {
	return int(percentOf(float64(s.matchCount), float64(s.transmitCount)))
}

func (s *journalStats) updatePosition() {
	s.pos = (s.pos + 1) % 100
	s.lineCount++
}

func (s *journalStats) updateLineMatched() {
	if !s.matched[s.pos] {
		s.matchCount++
		s.matched[s.pos] = true
	}
}

func (s *journalStats) updateLineTransmitted() {
	if !s.transmitted[s.pos] {
		s.transmitCount++
		s.transmitted[s.pos] = true
	}
}

func (s *journalStats) updateLineNotMatched() {
	if s.matched[s.pos] {
		s.matchCount--
		s.matched[s.pos] = false
	}
}

func (s *journalStats) updateLineNotTransmitted() {
	if s.transmitted[s.pos] {
		s.transmitCount--
		s.transmitted[s.pos] = false
	}
}

func percentOf(total float64, value float64) float64 {
	if total == 0 || total == value {
		return 100
	}
	return value / (total / 100.0)
}
