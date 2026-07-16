package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	config, err := Load(mapLookup(nil))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if config.Environment != "development" {
		t.Errorf("Environment = %q, want development", config.Environment)
	}
	if config.Address != "0.0.0.0:8080" {
		t.Errorf("Address = %q, want 0.0.0.0:8080", config.Address)
	}
	if config.PublicBaseURL.String() != "http://localhost:8080" {
		t.Errorf("PublicBaseURL = %q", config.PublicBaseURL)
	}
	if config.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %s", config.ShutdownTimeout)
	}
}

func TestLoadProduction(t *testing.T) {
	t.Parallel()

	config, err := Load(mapLookup(map[string]string{
		"APP_ENV":          "production",
		"HOST":             "127.0.0.1",
		"PORT":             "9090",
		"PUBLIC_BASE_URL":  "https://cabotcup.ca/",
		"DATABASE_URL":     "postgres://redacted",
		"SHUTDOWN_TIMEOUT": "25s",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if config.Address != "127.0.0.1:9090" {
		t.Errorf("Address = %q", config.Address)
	}
	if config.PublicBaseURL.String() != "https://cabotcup.ca" {
		t.Errorf("PublicBaseURL = %q", config.PublicBaseURL)
	}
	if config.ShutdownTimeout != 25*time.Second {
		t.Errorf("ShutdownTimeout = %s", config.ShutdownTimeout)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "environment", env: map[string]string{"APP_ENV": "preview"}, want: "APP_ENV"},
		{name: "port", env: map[string]string{"PORT": "70000"}, want: "PORT"},
		{name: "base URL", env: map[string]string{"PUBLIC_BASE_URL": "/relative"}, want: "PUBLIC_BASE_URL"},
		{name: "production HTTP", env: map[string]string{"APP_ENV": "production", "PUBLIC_BASE_URL": "http://cabotcup.ca", "DATABASE_URL": "postgres://redacted"}, want: "https"},
		{name: "production database", env: map[string]string{"APP_ENV": "production", "PUBLIC_BASE_URL": "https://cabotcup.ca"}, want: "DATABASE_URL"},
		{name: "staging database", env: map[string]string{"APP_ENV": "staging", "PUBLIC_BASE_URL": "https://next.cabotcup.ca"}, want: "DATABASE_URL"},
		{name: "duration", env: map[string]string{"SHUTDOWN_TIMEOUT": "0s"}, want: "SHUTDOWN_TIMEOUT"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(mapLookup(test.env))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
