package config

import "os"

// Env returns true when a given environment variable is set to "yes".
func Env(env string) bool {
	return "yes" == os.Getenv(env)
}

// Hostname returns the current hostname. It can be overriden with
// DTAIL_HOSTNAME_OVERRIDE environment variable (useful for integration tests).
// When DTAIL_INTEGRATION_TEST_RUN_MODE is set to "yes", it automatically
// returns "integrationtest" as the hostname.
func Hostname() (string, error) {
	// Check if we're in integration test mode
	if Env("DTAIL_INTEGRATION_TEST_RUN_MODE") {
		return "integrationtest", nil
	}
	
	// Check for manual hostname override
	hostname := os.Getenv("DTAIL_HOSTNAME_OVERRIDE")
	if len(hostname) > 0 {
		return hostname, nil
	}
	
	// Return actual hostname
	return os.Hostname()
}
