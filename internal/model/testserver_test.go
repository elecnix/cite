package model

import (
	"net/http"
	"net/http/httptest"
)

func newTestServer(h http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(h)
}
