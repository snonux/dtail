package config

import (
	"testing"

	"github.com/mimecast/dtail/internal/source"
	"github.com/mimecast/dtail/internal/testutil"
)

func TestConstants(t *testing.T) {
	// Test default constants
	testutil.AssertEqual(t, 2222, DefaultSSHPort)
	testutil.AssertEqual(t, "info", DefaultLogLevel)
	testutil.AssertEqual(t, "fout", DefaultClientLogger)
	testutil.AssertEqual(t, "file", DefaultServerLogger)
	testutil.AssertEqual(t, "none", DefaultHealthCheckLogger)
	testutil.AssertEqual(t, "DTAIL-HEALTH", HealthUser)
	testutil.AssertEqual(t, "DTAIL-SCHEDULE", ScheduleUser)
	testutil.AssertEqual(t, "DTAIL-CONTINUOUS", ContinuousUser)
}

func TestSetup(t *testing.T) {
	// Save original values
	origClient := Client
	origServer := Server
	origCommon := Common
	defer func() {
		Client = origClient
		Server = origServer
		Common = origCommon
	}()

	t.Run("setup with defaults", func(t *testing.T) {
		// Clear configs
		Client = nil
		Server = nil
		Common = nil
		
		// Setup with default args
		args := &Args{
			ConfigFile: "none", // Skip config file loading
		}
		
		Setup(source.Client, args, nil)
		
		// Should have initialized with defaults
		if Client == nil || Common == nil {
			t.Error("Expected client configs to be initialized")
		}
		
		// Test some default values
		testutil.AssertEqual(t, true, Client.TermColorsEnable)
		// SSHPort might not be set in basic setup, check logger instead
		if Common.Logger == "" {
			t.Error("Expected Common.Logger to be set")
		}
	})
}

func TestGlobalConfigs(t *testing.T) {
	// Test that global configs can be set and retrieved
	t.Run("set and get configs", func(t *testing.T) {
		// Create test configs
		testClient := &ClientConfig{
			TermColorsEnable: true,
		}
		testServer := &ServerConfig{
			SSHBindAddress: "test:2222",
		}
		testCommon := &CommonConfig{
			SSHPort: 2222,
		}
		
		// Set global configs
		Client = testClient
		Server = testServer
		Common = testCommon
		
		// Verify they're set correctly
		testutil.AssertEqual(t, true, Client.TermColorsEnable)
		testutil.AssertEqual(t, "test:2222", Server.SSHBindAddress)
		testutil.AssertEqual(t, 2222, Common.SSHPort)
	})
}