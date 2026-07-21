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
	Environment       string
	Address           string
	PublicBaseURL     *url.URL
	PrivateAppEnabled bool
	DatabaseMode      string
	DatabaseURL       string
	OIDCIssuerURL     string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	SessionTTL        time.Duration
	LoginAttemptTTL   time.Duration
	ShutdownTimeout   time.Duration
	// WagerAutoApproveMaxCents is the largest stake (in cents) that is accepted
	// automatically on placement; larger wagers wait for manual admin approval.
	// Zero disables auto-approval entirely.
	WagerAutoApproveMaxCents int64
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

	autoApproveMaxCents, err := parseAutoApproveThreshold(valueOrDefault(lookup, "WAGER_AUTO_APPROVE_MAX_CENTS", strconv.FormatInt(defaultAutoApproveMaxCents, 10)))
	if err != nil {
		return Config{}, err
	}

	databaseMode, databaseURL, err := DatabaseSelection(lookup)
	if err != nil {
		return Config{}, err
	}
	privateEnabled, err := strconv.ParseBool(valueOrDefault(lookup, "PRIVATE_APP_ENABLED", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("PRIVATE_APP_ENABLED must be true or false")
	}

	sessionTTL, err := parseBoundedDuration(
		"SESSION_TTL", valueOrDefault(lookup, "SESSION_TTL", "12h"), time.Minute, 7*24*time.Hour,
	)
	if err != nil {
		return Config{}, err
	}
	loginAttemptTTL, err := parseBoundedDuration(
		"LOGIN_ATTEMPT_TTL", valueOrDefault(lookup, "LOGIN_ATTEMPT_TTL", "10m"), time.Minute, 30*time.Minute,
	)
	if err != nil {
		return Config{}, err
	}

	oidcIssuerURL := strings.TrimSpace(value(lookup, "OIDC_ISSUER_URL"))
	oidcClientID := strings.TrimSpace(value(lookup, "OIDC_CLIENT_ID"))
	oidcClientSecret := strings.TrimSpace(value(lookup, "OIDC_CLIENT_SECRET"))
	oidcRedirectURL := strings.TrimSpace(value(lookup, "OIDC_REDIRECT_URL"))
	if privateEnabled {
		if databaseURL == "" {
			return Config{}, fmt.Errorf("DATABASE_URL is required when PRIVATE_APP_ENABLED is true")
		}
		if databaseMode == DatabaseModeTest && environment == "production" {
			return Config{}, fmt.Errorf("DATABASE_MODE=test is not allowed in production; use staging or an acceptance hostname to rehearse")
		}
		if err := validateOIDCConfig(environment, publicBaseURL, oidcIssuerURL, oidcClientID, oidcClientSecret, oidcRedirectURL); err != nil {
			return Config{}, err
		}
	}

	return Config{
		Environment:              environment,
		Address:                  net.JoinHostPort(host, strconv.Itoa(port)),
		PublicBaseURL:            publicBaseURL,
		PrivateAppEnabled:        privateEnabled,
		DatabaseMode:             databaseMode,
		DatabaseURL:              databaseURL,
		OIDCIssuerURL:            oidcIssuerURL,
		OIDCClientID:             oidcClientID,
		OIDCClientSecret:         oidcClientSecret,
		OIDCRedirectURL:          oidcRedirectURL,
		SessionTTL:               sessionTTL,
		LoginAttemptTTL:          loginAttemptTTL,
		ShutdownTimeout:          shutdownTimeout,
		WagerAutoApproveMaxCents: autoApproveMaxCents,
	}, nil
}

// defaultAutoApproveMaxCents is the default largest auto-approved stake: $100.
const defaultAutoApproveMaxCents = 10_000

func parseAutoApproveThreshold(value string) (int64, error) {
	cents, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || cents < 0 {
		return 0, fmt.Errorf("WAGER_AUTO_APPROVE_MAX_CENTS must be a non-negative integer number of cents")
	}
	return cents, nil
}

const (
	DatabaseModeReal = "real"
	DatabaseModeTest = "test"
)

// DatabaseSelection resolves which PostgreSQL database every command (server,
// migrate, legacy import) targets. DATABASE_MODE=test flips the whole process
// onto TEST_DATABASE_URL so workflows can be rehearsed end to end without
// touching the real ledger; the two URLs must differ so a mislabeled secret
// cannot silently point "test" at real money.
func DatabaseSelection(lookup func(string) (string, bool)) (mode string, databaseURL string, err error) {
	mode = valueOrDefault(lookup, "DATABASE_MODE", DatabaseModeReal)
	if mode != DatabaseModeReal && mode != DatabaseModeTest {
		return "", "", fmt.Errorf("DATABASE_MODE must be %q or %q", DatabaseModeReal, DatabaseModeTest)
	}
	realURL := strings.TrimSpace(value(lookup, "DATABASE_URL"))
	testURL := strings.TrimSpace(value(lookup, "TEST_DATABASE_URL"))
	if mode == DatabaseModeReal {
		return mode, realURL, nil
	}
	if testURL == "" {
		return "", "", fmt.Errorf("TEST_DATABASE_URL is required when DATABASE_MODE is test")
	}
	if testURL == realURL {
		return "", "", fmt.Errorf("TEST_DATABASE_URL must differ from DATABASE_URL")
	}
	return mode, testURL, nil
}

func value(lookup func(string) (string, bool), key string) string {
	result, _ := lookup(key)
	return result
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

func parseBoundedDuration(name, raw string, minimum, maximum time.Duration) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration < minimum || duration > maximum {
		return 0, fmt.Errorf("%s must be between %s and %s", name, minimum, maximum)
	}
	return duration, nil
}

func validateOIDCConfig(environment string, publicBaseURL *url.URL, issuer, clientID, clientSecret, redirect string) error {
	if issuer == "" || clientID == "" || clientSecret == "" || redirect == "" {
		return fmt.Errorf("OIDC issuer, client ID, client secret, and redirect URL are required when PRIVATE_APP_ENABLED is true")
	}
	issuerURL, err := url.Parse(issuer)
	if err != nil || issuerURL.Scheme == "" || issuerURL.Host == "" || issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
		return fmt.Errorf("OIDC_ISSUER_URL must be an absolute URL without a query or fragment")
	}
	redirectURL, err := url.Parse(redirect)
	if err != nil || redirectURL.Scheme == "" || redirectURL.Host == "" || redirectURL.RawQuery != "" || redirectURL.Fragment != "" {
		return fmt.Errorf("OIDC_REDIRECT_URL must be an absolute URL without a query or fragment")
	}
	if isDeployed(environment) && (issuerURL.Scheme != "https" || redirectURL.Scheme != "https") {
		return fmt.Errorf("OIDC issuer and redirect URLs must use https in staging and production")
	}
	if !strings.EqualFold(redirectURL.Host, publicBaseURL.Host) {
		return fmt.Errorf("OIDC_REDIRECT_URL must use the PUBLIC_BASE_URL host")
	}
	if redirectURL.Path != "/auth/callback" {
		return fmt.Errorf("OIDC_REDIRECT_URL path must be /auth/callback")
	}
	return nil
}
