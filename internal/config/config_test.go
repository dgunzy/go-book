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
	if config.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty for public mode", config.DatabaseURL)
	}
}

func TestLoadPrivateApplication(t *testing.T) {
	t.Parallel()

	config, err := Load(mapLookup(map[string]string{
		"APP_ENV":             "production",
		"PUBLIC_BASE_URL":     "https://cabotcup.ca",
		"PRIVATE_APP_ENABLED": "true",
		"DATABASE_URL":        "postgres://redacted",
		"OIDC_ISSUER_URL":     "https://accounts.google.com",
		"OIDC_CLIENT_ID":      "client-id",
		"OIDC_CLIENT_SECRET":  "client-secret",
		"OIDC_REDIRECT_URL":   "https://cabotcup.ca/auth/callback",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !config.PrivateAppEnabled || config.SessionTTL != 12*time.Hour || config.LoginAttemptTTL != 10*time.Minute {
		t.Fatalf("private config = %+v", config)
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
		{name: "production HTTP", env: map[string]string{"APP_ENV": "production", "PUBLIC_BASE_URL": "http://cabotcup.ca"}, want: "https"},
		{name: "private database", env: map[string]string{"PRIVATE_APP_ENABLED": "true"}, want: "DATABASE_URL"},
		{name: "private OIDC", env: map[string]string{"PRIVATE_APP_ENABLED": "true", "DATABASE_URL": "postgres://redacted"}, want: "OIDC"},
		{name: "private redirect host", env: privateConfig(map[string]string{"OIDC_REDIRECT_URL": "https://other.example/auth/callback"}), want: "PUBLIC_BASE_URL host"},
		{name: "session TTL", env: map[string]string{"SESSION_TTL": "8d"}, want: "SESSION_TTL"},
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

func privateConfig(overrides map[string]string) map[string]string {
	values := map[string]string{
		"APP_ENV":             "production",
		"PUBLIC_BASE_URL":     "https://cabotcup.ca",
		"PRIVATE_APP_ENABLED": "true",
		"DATABASE_URL":        "postgres://redacted",
		"OIDC_ISSUER_URL":     "https://accounts.google.com",
		"OIDC_CLIENT_ID":      "client-id",
		"OIDC_CLIENT_SECRET":  "client-secret",
		"OIDC_REDIRECT_URL":   "https://cabotcup.ca/auth/callback",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return values
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
