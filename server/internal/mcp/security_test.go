package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRedirectCannotForwardSecret(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	c := NewClient(first.URL, map[string]string{"X-API-Key": "test-secret"})
	if _, err := c.Tools(context.Background()); err == nil {
		t.Fatal("cross-origin redirect succeeded")
	}
	if reached.Load() {
		t.Fatal("request reached the foreign server")
	}
}
