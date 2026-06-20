// Package test provides test utilities for the jigsawstack package.
package test

import (
	"log/slog"
	"os"
	"testing"
)

// DefaultLogger is a default logger for tests.
var DefaultLogger = slog.Default()

// IsIntegrationTest returns true if the test is an integration test.
func IsIntegrationTest() bool {
	return os.Getenv("INTEGRATION_TEST") == "true" || testing.Short()
}

// GetAPIKey returns the API key from the environment variable.
func GetAPIKey(envVar string) (string, error) {
	key := os.Getenv(envVar)
	if key == "" {
		return "", nil
	}
	return key, nil
}
