package config

import (
	"os"
	"testing"

	"github.com/mimecast/dtail/internal/testutil"
)

func TestEnv(t *testing.T) {
	t.Run("env var set to yes", func(t *testing.T) {
		// Set a test env var
		os.Setenv("TEST_ENV_VAR", "yes")
		defer os.Unsetenv("TEST_ENV_VAR")
		
		value := Env("TEST_ENV_VAR")
		testutil.AssertEqual(t, true, value)
	})

	t.Run("env var set to other value", func(t *testing.T) {
		// Set to something other than "yes"
		os.Setenv("TEST_ENV_VAR", "no")
		defer os.Unsetenv("TEST_ENV_VAR")
		
		value := Env("TEST_ENV_VAR")
		testutil.AssertEqual(t, false, value)
	})

	t.Run("non-existing env var", func(t *testing.T) {
		// Make sure it doesn't exist
		os.Unsetenv("NON_EXISTING_VAR")
		
		value := Env("NON_EXISTING_VAR")
		testutil.AssertEqual(t, false, value)
	})

	t.Run("empty env var", func(t *testing.T) {
		// Set empty value
		os.Setenv("EMPTY_VAR", "")
		defer os.Unsetenv("EMPTY_VAR")
		
		value := Env("EMPTY_VAR")
		testutil.AssertEqual(t, false, value)
	})
}

func TestHostname(t *testing.T) {
	t.Run("default hostname", func(t *testing.T) {
		// Clear any override
		os.Unsetenv("DTAIL_HOSTNAME_OVERRIDE")
		
		hostname, err := Hostname()
		testutil.AssertNoError(t, err)
		// Should return actual hostname (non-empty)
		if hostname == "" {
			t.Error("Expected non-empty hostname")
		}
	})

	t.Run("hostname override", func(t *testing.T) {
		// Set override
		os.Setenv("DTAIL_HOSTNAME_OVERRIDE", "test-host")
		defer os.Unsetenv("DTAIL_HOSTNAME_OVERRIDE")
		
		hostname, err := Hostname()
		testutil.AssertNoError(t, err)
		testutil.AssertEqual(t, "test-host", hostname)
	})

	t.Run("empty hostname override", func(t *testing.T) {
		// Set empty override
		os.Setenv("DTAIL_HOSTNAME_OVERRIDE", "")
		defer os.Unsetenv("DTAIL_HOSTNAME_OVERRIDE")
		
		hostname, err := Hostname()
		testutil.AssertNoError(t, err)
		// Should return actual hostname (non-empty)
		if hostname == "" {
			t.Error("Expected non-empty hostname when override is empty")
		}
	})
}