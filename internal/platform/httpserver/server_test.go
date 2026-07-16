package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

func TestRunReturnsWhenContextIsCancelled(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(Config{Address: "127.0.0.1:8080", ShutdownTimeout: time.Second}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), logger)
	started := make(chan struct{})
	stopped := make(chan struct{})
	server.serve = func() error {
		close(started)
		<-stopped
		return http.ErrServerClosed
	}
	server.shutdown = func(context.Context) error {
		close(stopped)
		return nil
	}
	server.close = func() error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Run() did not start serving")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}
