package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort            = 8080
	defaultShutdownTimeout = 10 * time.Second
)

// Config contains process-level settings. Secrets remain in their dedicated fields so
// callers can avoid logging the whole structure.
type Config struct {
	Environment     string
	Address         string
	PublicBaseURL   *url.URL
	DatabaseURL     string
	ShutdownTimeout time.Duration
}

// Load reads and validates configuration using lookup. Passing os.LookupEnv keeps
// startup conventional while allowing tests to avoid mutating process state.
func Load(lookup func(string) (string, bool)) (Config, error) {
	environment := valueOrDefault(lookup, "APP_ENV", "development")
	if environment != "development" && environment != "test" && environment != "staging" && environment != "production" {
		return Config{}, fmt.Errorf("APP_ENV must be development, test, staging, or production")
	}

	port, err := parsePort(valueOrDefault(lookup, "PORT", strconv.Itoa(defaultPort)))
	if err != nil {
		return Config{}, err
	}

	host := valueOrDefault(lookup, "HOST", "0.0.0.0")
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return Config{}, fmt.Errorf("HOST must be a hostname or IP address without a port")
	}

	publicBaseURL, err := parseBaseURL(valueOrDefault(
		lookup,
		"PUBLIC_BASE_URL",
		fmt.Sprintf("http://localhost:%d", port),
	), environment)
	if err != nil {
		return Config{}, err
	}

	shutdownTimeout, err := parseDuration(
		"SHUTDOWN_TIMEOUT",
		valueOrDefault(lookup, "SHUTDOWN_TIMEOUT", defaultShutdownTimeout.String()),
	)
	if err != nil {
		return Config{}, err
	}

	databaseURL, _ := lookup("DATABASE_URL")
	if isDeployed(environment) && strings.TrimSpace(databaseURL) == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required in staging and production")
	}

	return Config{
		Environment:     environment,
		Address:         net.JoinHostPort(host, strconv.Itoa(port)),
		PublicBaseURL:   publicBaseURL,
		DatabaseURL:     databaseURL,
		ShutdownTimeout: shutdownTimeout,
	}, nil
}

func valueOrDefault(lookup func(string) (string, bool), key, fallback string) string {
	if value, ok := lookup(key); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT must be an integer between 1 and 65535")
	}
	return port, nil
}

func parseBaseURL(raw, environment string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL must be an absolute URL without a query or fragment")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL scheme must be http or https")
	}
	if isDeployed(environment) && parsed.Scheme != "https" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL must use https in staging and production")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed, nil
}

func isDeployed(environment string) bool {
	return environment == "staging" || environment == "production"
}

func parseDuration(name, raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return duration, nil
}
