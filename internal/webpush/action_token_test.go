package webpush

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestActionTokenClaimsAndConsumesOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tokens, err := newActionTokens(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 128)), func() time.Time { return now })
	if err != nil {
		t.Fatalf("new tokens: %v", err)
	}
	token, err := tokens.mint("alice", "job-1", "interaction-1", []string{"allow", "reject"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims, err := tokens.consume(token, "allow")
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if claims.CallerID != "alice" || claims.JobID != "job-1" || claims.InteractionID != "interaction-1" {
		t.Fatalf("claims = %#v", claims)
	}
	if _, err := tokens.consume(token, "allow"); !errors.Is(err, ErrActionUsed) {
		t.Fatalf("replay error = %v, want ErrActionUsed", err)
	}

	optionToken, _ := tokens.mint("alice", "job-1", "interaction-2", []string{"allow"})
	if _, err := tokens.consume(optionToken, "reject"); !errors.Is(err, ErrActionOption) {
		t.Fatalf("option error = %v, want ErrActionOption", err)
	}
	if _, err := tokens.consume(optionToken+"x", "allow"); !errors.Is(err, ErrActionUnauthorized) {
		t.Fatalf("tamper error = %v, want ErrActionUnauthorized", err)
	}

	expiring, _ := tokens.mint("alice", "job-1", "interaction-3", []string{"allow"})
	now = now.Add(11 * time.Minute)
	if _, err := tokens.consume(expiring, "allow"); !errors.Is(err, ErrActionExpired) {
		t.Fatalf("expiry error = %v, want ErrActionExpired", err)
	}
}

func TestPushActionRejectsRemovedCaller(t *testing.T) {
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	callers := []string{"alice"}
	service, err := NewService(Options{
		Store: store, Jobs: &dispatchJobs{}, ConfigDir: t.TempDir(),
		UserCallers: func() []string { return callers },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()
	token, err := service.NewActionToken("alice", "job-1", "interaction-1", []string{"allow"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	callers = nil // alice removed from config after the notification went out
	if _, err := service.Act(token, "allow"); !errors.Is(err, ErrActionUnauthorized) {
		t.Fatalf("act error = %v, want ErrActionUnauthorized", err)
	}
}
