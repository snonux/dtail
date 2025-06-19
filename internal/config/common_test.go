package config

import (
	"testing"

	"github.com/mimecast/dtail/internal/testutil"
)

func TestCommonConfig(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		c := CommonConfig{}
		
		// Test zero values
		testutil.AssertEqual(t, 0, c.SSHPort)
		testutil.AssertEqual(t, "", c.LogDir)
		testutil.AssertEqual(t, "", c.LogLevel)
		testutil.AssertEqual(t, "", c.LogRotation)
		testutil.AssertEqual(t, false, c.ExperimentalFeaturesEnable)
		testutil.AssertEqual(t, "", c.CacheDir)
	})

	t.Run("setter methods", func(t *testing.T) {
		c := CommonConfig{}
		
		// Set values
		c.SSHPort = 2222
		c.LogDir = "/var/log/dtail"
		c.LogLevel = "debug"
		c.LogRotation = "daily"
		c.ExperimentalFeaturesEnable = true
		c.CacheDir = "/tmp/dtail-cache"
		
		// Verify values
		testutil.AssertEqual(t, 2222, c.SSHPort)
		testutil.AssertEqual(t, "/var/log/dtail", c.LogDir)
		testutil.AssertEqual(t, "debug", c.LogLevel)
		testutil.AssertEqual(t, "daily", c.LogRotation)
		testutil.AssertEqual(t, true, c.ExperimentalFeaturesEnable)
		testutil.AssertEqual(t, "/tmp/dtail-cache", c.CacheDir)
	})

	t.Run("default config", func(t *testing.T) {
		c := newDefaultCommonConfig()
		
		testutil.AssertEqual(t, DefaultSSHPort, c.SSHPort)
		testutil.AssertEqual(t, "log", c.LogDir)
		testutil.AssertEqual(t, "stdout", c.Logger)
		testutil.AssertEqual(t, DefaultLogLevel, c.LogLevel)
		testutil.AssertEqual(t, "daily", c.LogRotation)
		testutil.AssertEqual(t, "cache", c.CacheDir)
		testutil.AssertEqual(t, false, c.ExperimentalFeaturesEnable)
	})
}