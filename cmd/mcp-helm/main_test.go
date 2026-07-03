package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunHealthcheck(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		t.Setenv("MCP_HELM_HEALTHCHECK_URL", server.URL)

		if err := runHealthcheck(); err != nil {
			t.Fatalf("runHealthcheck() error = %v", err)
		}
	})

	t.Run("unhealthy status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		t.Setenv("MCP_HELM_HEALTHCHECK_URL", server.URL)

		if err := runHealthcheck(); err == nil {
			t.Fatal("runHealthcheck() error = nil, want non-nil")
		}
	})
}
