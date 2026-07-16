package httpmiddleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestRequestIDAddsHeaderAndContext(t *testing.T) {
	t.Parallel()

	var contextID string
	handler := RequestID(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		contextID = RequestIDFromContext(request.Context())
		response.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	headerID := response.Header().Get("X-Request-ID")
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(headerID) {
		t.Fatalf("X-Request-ID = %q", headerID)
	}
	if contextID != headerID {
		t.Errorf("context request ID = %q, want %q", contextID, headerID)
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	handler := SecurityHeaders(true)(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	for _, header := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options", "Permissions-Policy", "Strict-Transport-Security"} {
		if response.Header().Get(header) == "" {
			t.Errorf("header %s was not set", header)
		}
	}
}

func TestRecoverReturnsInternalServerError(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}
