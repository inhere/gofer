package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/serve/healthcheck"
)

// failingProbe is a probe whose outcome the test flips between runs.
type failingProbe struct{ err error }

func (p *failingProbe) probe() error { return p.err }

// readJSONBody decodes a response body into a generic map for the field assertions below.
func readJSONBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// TestHealthChecksEndpointListsItems: GET /v1/health/checks returns every configured item
// with its counters, and a caller only needs to be logged in (design 2026-11-14 §5).
func TestHealthChecksEndpointListsItems(t *testing.T) {
	s := newTestServer(t, testToken, false)
	stub := &failingProbe{err: errors.New("disk full")}
	runner := healthcheck.New(3,
		healthcheck.Check{Name: healthcheck.CheckStorage, Probe: func() error { return nil }},
		healthcheck.Check{Name: healthcheck.CheckDisk, Probe: stub.probe},
	)
	runner.RunOnce()
	runner.RunOnce()
	s.SetHealthChecks(runner)

	resp := do(t, s, http.MethodGet, "/v1/health/checks", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/health/checks = %d, want 200", resp.StatusCode)
	}
	body := readJSONBody(t, resp)
	checks, ok := body["checks"].([]any)
	if !ok || len(checks) != 2 {
		t.Fatalf("checks = %v, want 2 items", body["checks"])
	}
	disk, ok := checks[1].(map[string]any)
	if !ok {
		t.Fatalf("second check = %v, want an object", checks[1])
	}
	if disk["name"] != healthcheck.CheckDisk {
		t.Fatalf("second check name = %v, want %q", disk["name"], healthcheck.CheckDisk)
	}
	if disk["ok"] != false || disk["consecutive_failures"] != float64(2) || disk["failing"] != false {
		t.Fatalf("two misses = %v, want ok=false, count 2, not yet failing", disk)
	}
	if disk["last_error"] != "disk full" {
		t.Fatalf("last_error = %v, want the probe error", disk["last_error"])
	}
	if at, _ := disk["checked_at"].(float64); at == 0 {
		t.Fatalf("checked_at = %v, want the run stamp", disk["checked_at"])
	}

	// Without a bearer token the items are not readable (they are login-only).
	resp = do(t, s, http.MethodGet, "/v1/health/checks", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /v1/health/checks = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestHealthDegradedBit: /health stays public and says only whether some item is failing,
// while /v1/stats names the failing items.
func TestHealthDegradedBit(t *testing.T) {
	s := newTestServer(t, testToken, false)
	stub := &failingProbe{err: errors.New("boom")}
	runner := healthcheck.New(3, healthcheck.Check{Name: healthcheck.CheckStorage, Probe: stub.probe})
	s.SetHealthChecks(runner)

	// Healthy: degraded=false, no health_failing on the stats payload.
	runner.RunOnce()
	body := readJSONBody(t, do(t, s, http.MethodGet, "/health", "", nil))
	if body["degraded"] != false {
		t.Fatalf("/health degraded = %v, want false while healthy", body["degraded"])
	}
	stats := readJSONBody(t, do(t, s, http.MethodGet, "/v1/stats", testToken, nil))
	if _, present := stats["health_failing"]; present {
		t.Fatalf("/v1/stats carries health_failing while healthy: %v", stats["health_failing"])
	}

	// Third consecutive failure: degraded=true, and /health carries no detail.
	runner.RunOnce()
	runner.RunOnce()
	body = readJSONBody(t, do(t, s, http.MethodGet, "/health", "", nil))
	if body["degraded"] != true {
		t.Fatalf("/health degraded = %v, want true after the threshold", body["degraded"])
	}
	if _, leaked := body["checks"]; leaked {
		t.Fatalf("/health must not carry the item detail: %v", body)
	}
	stats = readJSONBody(t, do(t, s, http.MethodGet, "/v1/stats", testToken, nil))
	failing, ok := stats["health_failing"].([]any)
	if !ok || len(failing) != 1 || failing[0] != healthcheck.CheckStorage {
		t.Fatalf("/v1/stats health_failing = %v, want [storage]", stats["health_failing"])
	}

	// One success recovers: the bit clears and the stats list disappears.
	stub.err = nil
	runner.RunOnce()
	body = readJSONBody(t, do(t, s, http.MethodGet, "/health", "", nil))
	if body["degraded"] != false {
		t.Fatalf("/health degraded = %v, want false after one success", body["degraded"])
	}
}

// TestHealthDegradedWithoutRunner: a server that wired no self-check (tests, mcp) still
// answers both endpoints, as healthy and with an empty item list.
func TestHealthDegradedWithoutRunner(t *testing.T) {
	s := newTestServer(t, testToken, false)
	body := readJSONBody(t, do(t, s, http.MethodGet, "/health", "", nil))
	if body["degraded"] != false {
		t.Fatalf("/health degraded = %v, want false with no checks wired", body["degraded"])
	}
	resp := do(t, s, http.MethodGet, "/v1/health/checks", testToken, nil)
	body = readJSONBody(t, resp)
	if checks, ok := body["checks"].([]any); !ok || len(checks) != 0 {
		t.Fatalf("checks = %v, want an empty list", body["checks"])
	}
}
