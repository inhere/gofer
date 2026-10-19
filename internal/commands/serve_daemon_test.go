package commands

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeHealthURL(t *testing.T) {
	old := serveOpts.addr
	t.Cleanup(func() { serveOpts.addr = old })
	for addr, want := range map[string]string{
		"0.0.0.0:9001":   "http://127.0.0.1:9001/health",
		":9002":          "http://127.0.0.1:9002/health",
		"10.0.0.5:9003":  "http://10.0.0.5:9003/health",
		"[::]:9004":      "http://127.0.0.1:9004/health",
		"not-an-address": "",
	} {
		serveOpts.addr = addr
		if got := serveHealthURL(); got != want {
			t.Errorf("serveHealthURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestHealthOK(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	bad := httptest.NewServer(http.NotFoundHandler())
	defer bad.Close()
	if !healthOK(ok.URL) || healthOK(bad.URL) || healthOK("http://127.0.0.1:1/health") {
		t.Fatal("healthOK must be true only for a 200 answer")
	}
}
