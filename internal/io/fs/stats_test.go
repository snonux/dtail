package fs

import (
	"testing"

	"github.com/mimecast/dtail/internal/constants"
	"github.com/mimecast/dtail/internal/testutil"
)

func TestStats(t *testing.T) {
	s := &stats{}

	// Test initial state
	testutil.AssertEqual(t, uint64(0), s.totalLineCount())
	// With no matches, percentage should be 100% (special case)
	testutil.AssertEqual(t, 100, s.transmittedPerc())

	// Test updating position and line count
	s.updatePosition()
	testutil.AssertEqual(t, uint64(1), s.totalLineCount())

	// Test match and transmit tracking
	s.updateLineMatched()
	testutil.AssertEqual(t, uint64(1), s.matchCount)
	
	s.updateLineTransmitted()
	testutil.AssertEqual(t, 1, s.transmitCount)
	testutil.AssertEqual(t, 100, s.transmittedPerc()) // 1/1 = 100%

	// Test multiple lines
	for i := 0; i < 5; i++ {
		s.updatePosition()
		s.updateLineMatched()
	}
	testutil.AssertEqual(t, uint64(6), s.totalLineCount())
	testutil.AssertEqual(t, uint64(6), s.matchCount)
	testutil.AssertEqual(t, 1, s.transmitCount)
	
	// Transmit percentage should be 1/6 = 16.666... ≈ 16
	perc := s.transmittedPerc()
	if perc < 16 || perc > 17 {
		t.Errorf("expected transmitted percentage around 16-17, got %d", perc)
	}
}

func TestStatsCircularBuffer(t *testing.T) {
	s := &stats{}

	// Fill the circular buffer
	for i := 0; i < constants.StatsArraySize+10; i++ {
		s.updatePosition()
		if i%2 == 0 {
			s.updateLineMatched()
			s.updateLineTransmitted()
		}
	}

	// Should have wrapped around
	testutil.AssertEqual(t, uint64(constants.StatsArraySize+10), s.totalLineCount())
	
	// The array should be tracking only the last StatsArraySize entries
	// Since we're alternating matched/transmitted, we should have roughly 50%
	perc := s.transmittedPerc()
	if perc < 90 || perc > 110 {
		t.Errorf("expected transmitted percentage around 100%%, got %d", perc)
	}
}

func TestStatsUpdateNotMatched(t *testing.T) {
	s := &stats{}

	// Set up initial state
	s.updatePosition()
	s.updateLineMatched()
	s.updateLineTransmitted()
	testutil.AssertEqual(t, uint64(1), s.matchCount)
	testutil.AssertEqual(t, 1, s.transmitCount)

	// Update to not matched/transmitted
	s.updateLineNotMatched()
	s.updateLineNotTransmitted()
	testutil.AssertEqual(t, uint64(0), s.matchCount)
	testutil.AssertEqual(t, 0, s.transmitCount)
}

func TestPercentOf(t *testing.T) {
	tests := []struct {
		name     string
		total    float64
		value    float64
		expected float64
	}{
		{"zero total", 0, 50, 100},
		{"equal values", 100, 100, 100},
		{"half", 100, 50, 50},
		{"quarter", 100, 25, 25},
		{"tenth", 100, 10, 10},
		{"over 100%", 100, 150, 150},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := percentOf(tt.total, tt.value)
			if result != tt.expected {
				t.Errorf("percentOf(%f, %f) = %f, want %f", 
					tt.total, tt.value, result, tt.expected)
			}
		})
	}
}