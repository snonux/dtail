package config

import "testing"

func TestDefaultServerResilienceLimits(t *testing.T) {
	cfg := newDefaultServerConfig()
	if cfg.IdleSessionTimeoutS != DefaultIdleSessionTimeoutS || cfg.IdleSessionTimeoutS <= 0 {
		t.Fatalf("IdleSessionTimeoutS = %d, want positive default %d",
			cfg.IdleSessionTimeoutS, DefaultIdleSessionTimeoutS)
	}
	if cfg.OutputBufferMaxBytes != DefaultOutputBufferMaxBytes || cfg.OutputBufferMaxBytes <= 0 {
		t.Fatalf("OutputBufferMaxBytes = %d, want positive default %d",
			cfg.OutputBufferMaxBytes, DefaultOutputBufferMaxBytes)
	}
}
