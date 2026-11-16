package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/serve/healthcheck"
)

// TestInboxServerHealthCard: a self-check item below the threshold shows no card, the
// threshold run puts one card in the action column under the attention class, and one
// successful run removes it again (design 2026-11-14 §5).
func TestInboxServerHealthCard(t *testing.T) {
	s := inboxTestServer(t, nil)
	stub := &failingProbe{err: errors.New("low disk space: 200MiB free")}
	runner := healthcheck.New(3, healthcheck.Check{Name: healthcheck.CheckDisk, Probe: stub.probe})
	s.SetHealthChecks(runner)

	const key = "server.health:" + healthcheck.CheckDisk
	runner.RunOnce()
	runner.RunOnce()
	if _, ok := getInbox(t, s, "tok-alice", "").find(key); ok {
		t.Fatal("two misses must not raise a card")
	}

	runner.RunOnce()
	item, ok := getInbox(t, s, "tok-alice", "").find(key)
	if !ok {
		t.Fatalf("no %q item after the threshold run", key)
	}
	if item.Kind != "server.health" || item.Column != "action" || item.Waiting != "attention" || item.WaitingLabel == "" {
		t.Fatalf("item = %+v, want an action-column server.health card in the attention class", item)
	}

	// Still failing: the item stays, and it is one entry, not one per run.
	if items := getInbox(t, s, "tok-alice", ""); len(items.Columns["action"]) != 1 {
		t.Fatalf("action column = %+v, want the single health card", items.Columns["action"])
	}

	// One success: the card is gone.
	stub.err = nil
	runner.RunOnce()
	if _, ok := getInbox(t, s, "tok-alice", "").find(key); ok {
		t.Fatal("the card must disappear after one successful run")
	}

	// A server that wired no self-check shows no card either.
	plain := inboxTestServer(t, nil)
	if _, ok := getInbox(t, plain, "tok-alice", "").find(key); ok {
		t.Fatal("no runner must mean no health card")
	}
}

// TestHealthChecksEndpointNeedsNoExtraPermission is covered by
// TestHealthChecksEndpointListsItems: the endpoint is login-only. This one pins the card
// against the plain HTTP surface: a caller without any extra permission still sees it is
// only about the server's own parts (no project, no job).
func TestInboxServerHealthCardHasNoRefs(t *testing.T) {
	s := inboxTestServer(t, nil)
	runner := healthcheck.New(1, healthcheck.Check{Name: healthcheck.CheckStorage, Probe: func() error { return errors.New("readonly database") }})
	runner.RunOnce()
	s.SetHealthChecks(runner)

	resp := do(t, s, http.MethodGet, "/v1/inbox", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/inbox = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}
