package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// startFakeProvider runs an OpenAI-compatible chat/completions fake and
// returns its server. *called is set on first request.
func startFakeProvider(t *testing.T, called *bool, response string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv
}
