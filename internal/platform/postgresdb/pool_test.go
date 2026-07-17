package postgresdb

import "testing"

func TestConfig(t *testing.T) {
	t.Parallel()

	config, err := Config("postgres://member:secret@db.example/cabot_cup?sslmode=require")
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	if config.MaxConns != 8 || config.MinConns != 1 {
		t.Fatalf("connections = %d/%d, want 8/1", config.MaxConns, config.MinConns)
	}
	if got := config.ConnConfig.RuntimeParams["application_name"]; got != "cabot-cup-web" {
		t.Fatalf("application_name = %q", got)
	}
	if got := config.ConnConfig.RuntimeParams["statement_timeout"]; got != "10s" {
		t.Fatalf("statement_timeout = %q", got)
	}
}

func TestConfigRejectsInvalidURL(t *testing.T) {
	t.Parallel()

	if _, err := Config("://not-a-url"); err == nil {
		t.Fatal("Config() error = nil, want invalid URL")
	}
}
