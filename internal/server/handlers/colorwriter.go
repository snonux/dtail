package handlers

import (
	"io"
	"github.com/mimecast/dtail/internal/color/brush"
	"github.com/mimecast/dtail/internal/config"
)

// ColorWriter wraps an io.Writer and applies colors to output
type ColorWriter struct {
	writer      io.Writer
	noColor     bool
	isFirstLine bool
}

// NewColorWriter creates a new ColorWriter
func NewColorWriter(writer io.Writer, noColor bool) *ColorWriter {
	return &ColorWriter{
		writer:      writer,
		noColor:     noColor,
		isFirstLine: true,
	}
}

// Write implements io.Writer, applying colors if enabled
func (cw *ColorWriter) Write(p []byte) (n int, err error) {
	if cw.noColor || !config.Client.TermColorsEnable {
		// No colors, write as-is
		return cw.writer.Write(p)
	}

	// Apply colors to the content
	// The brush.Colorfy function will handle the coloring
	inputStr := string(p)
	coloredStr := brush.Colorfy(inputStr)
	
	// Add color reset prefix for all lines except the first
	var outputBytes []byte
	if cw.isFirstLine {
		cw.isFirstLine = false
		outputBytes = []byte(coloredStr)
	} else {
		// Add color reset prefix: [39m[49m[49m[39m
		colorResetPrefix := "\x1b[39m\x1b[49m\x1b[49m\x1b[39m"
		outputBytes = make([]byte, len(colorResetPrefix)+len(coloredStr))
		copy(outputBytes, colorResetPrefix)
		copy(outputBytes[len(colorResetPrefix):], coloredStr)
	}
	
	// Write the colored output
	_, err = cw.writer.Write(outputBytes)
	if err != nil {
		return 0, err
	}
	
	// Return the original byte count to maintain compatibility
	// (the caller expects n to match len(p))
	return len(p), nil
}

// Close closes the ColorWriter and writes final color reset if needed
func (cw *ColorWriter) Close() error {
	if !cw.noColor && config.Client.TermColorsEnable && !cw.isFirstLine {
		// Write final color reset line
		colorResetPrefix := "\x1b[39m\x1b[49m\x1b[49m\x1b[39m"
		cw.writer.Write([]byte(colorResetPrefix))
	}
	return nil
}