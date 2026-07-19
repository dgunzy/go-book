package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunCommandRejectsUnknownCommand(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := runCommand(context.Background(), logger, []string{"unknown"}, func(string) (string, bool) {
		return "", false
	})
	if err == nil || err.Error() != "usage: cabot-cup [migrate|legacy-book-report|legacy-book-promote|bootstrap-owner]" {
		t.Fatalf("error = %v", err)
	}
}

func TestRunMigrationsRequiresDatabaseURL(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := runCommand(context.Background(), logger, []string{"migrate"}, func(string) (string, bool) {
		return "", false
	})
	if err == nil || err.Error() != "DATABASE_URL is required for migrations" {
		t.Fatalf("error = %v", err)
	}
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := buildHandler(http.NotFoundHandler(), logger, false)

	for _, path := range []string{"/livez", "/readyz"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if response.Body.String() != "ok\n" {
				t.Errorf("body = %q", response.Body.String())
			}
			if response.Header().Get("X-Request-ID") == "" {
				t.Error("X-Request-ID was not set")
			}
		})
	}
}

func TestUnknownRouteUsesPublicHandler(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	public := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/public" {
			t.Errorf("path = %q, want /public", request.URL.Path)
		}
		response.WriteHeader(http.StatusTeapot)
	})
	response := httptest.NewRecorder()
	buildHandler(public, logger, false).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/public", nil))

	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTeapot)
	}
}

func TestReadinessReportsDatabaseFailureWithoutLeakingIt(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := buildHandler(http.NotFoundHandler(), logger, false, func(context.Context) error {
		return errors.New("postgres password and host details")
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "not ready\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestNewOutboxDispatcherConstructsWithConsumers(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dispatcher, err := newOutboxDispatcher(nil, logger)
	if err != nil {
		t.Fatalf("newOutboxDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("newOutboxDispatcher() = nil dispatcher")
	}
}

func TestBootstrapOwnerValidatesInputBeforeConnecting(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing email", map[string]string{}, "BOOTSTRAP_OWNER_EMAIL is required"},
		{"malformed email", map[string]string{
			"BOOTSTRAP_OWNER_EMAIL": "not-an-email",
		}, "BOOTSTRAP_OWNER_EMAIL must be an email address"},
		{"missing display name", map[string]string{
			"BOOTSTRAP_OWNER_EMAIL": "owner@example.test",
		}, "BOOTSTRAP_OWNER_DISPLAY_NAME is required"},
		{"missing reason", map[string]string{
			"BOOTSTRAP_OWNER_EMAIL":        "owner@example.test",
			"BOOTSTRAP_OWNER_DISPLAY_NAME": "Owner",
		}, "BOOTSTRAP_OWNER_REASON is required"},
		{"missing database", map[string]string{
			"BOOTSTRAP_OWNER_EMAIL":        "owner@example.test",
			"BOOTSTRAP_OWNER_DISPLAY_NAME": "Owner",
			"BOOTSTRAP_OWNER_REASON":       "initial deployment",
		}, "DATABASE_URL is required to bootstrap an owner"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := runCommand(context.Background(), logger, []string{"bootstrap-owner"}, func(key string) (string, bool) {
				value, ok := testCase.env[key]
				return value, ok
			})
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}
