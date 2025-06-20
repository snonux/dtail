package handlers

import (
	"io"
	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
)

// ColorWriter wraps an io.Writer and applies colors to output
type ColorWriter struct {
	writer io.Writer
	noColor bool
}

// NewColorWriter creates a new ColorWriter
func NewColorWriter(writer io.Writer, noColor bool) *ColorWriter {
	return &ColorWriter{
		writer: writer,
		noColor: noColor,
	}
}

// Write implements io.Writer, applying colors if enabled
func (cw *ColorWriter) Write(p []byte) (n int, err error) {
	if cw.noColor || !config.Client.TermColorsEnable {
		// No colors, write as-is
		return cw.writer.Write(p)
	}

	// Apply colors
	coloredStr := brush.Colorfy(string(p))
	coloredBytes := []byte(coloredStr)
	
	// Write the colored output
	_, err = cw.writer.Write(coloredBytes)
	if err != nil {
		return 0, err
	}
	
	// Return the original byte count to maintain compatibility
	// (the caller expects n to match len(p))
	return len(p), nil
}