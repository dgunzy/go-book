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
	if err == nil || err.Error() != "usage: cabot-cup [migrate|legacy-book-report|legacy-book-promote]" {
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
