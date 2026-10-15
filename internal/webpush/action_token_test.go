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
		MayAct:      func(string) bool { return true },
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

// TestPushServiceReadsTheUserListLive: the user list is a callback, not a startup snapshot —
// the property the assembly's config reload relies on (design §6「推送通知的用户列表在配置重载时
// 刷新」): a caller added by a reload becomes one and a caller removed by it stops being one,
// without a serve restart.
func TestPushServiceReadsTheUserListLive(t *testing.T) {
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	callers := []string{"alice"}
	service, err := NewService(Options{
		Store: store, Jobs: &dispatchJobs{}, ConfigDir: t.TempDir(),
		UserCallers: func() []string { return callers },
		MayAct:      func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()

	if !service.hasUserCaller("alice") || service.hasUserCaller("bob") {
		t.Fatal("the initial list must be exactly [alice]")
	}
	callers = []string{"alice", "bob"} // a caller added by a config reload
	if !service.hasUserCaller("bob") {
		t.Fatal("a caller added by a reload must be a user caller")
	}
	callers = nil // and one removed by it
	if service.hasUserCaller("alice") {
		t.Fatal("a caller removed by a reload must stop being a user caller")
	}
}

// TestPushActionChecksTheAnswerPermission is the second 旁路关闭 case of plan T1.5b (design
// §3「有意不保持现状」3): a notification's action button is for a caller holding the answer
// permission, checked when the button is MINTED and again when its token is REDEEMED, so a
// grant revoked between the two stops the button without waiting for a new notification.
func TestPushActionChecksTheAnswerPermission(t *testing.T) {
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	allowed := false
	service, err := NewService(Options{
		Store: store, Jobs: &dispatchJobs{}, ConfigDir: t.TempDir(),
		UserCallers: func() []string { return []string{"alice"} },
		MayAct:      func(string) bool { return allowed },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()

	// Minting: a caller that may not answer gets no button at all.
	if _, err := service.NewActionToken("alice", "job-1", "interaction-1", []string{"allow"}); !errors.Is(err, ErrActionForbidden) {
		t.Fatalf("mint without the permission = %v, want ErrActionForbidden", err)
	}

	// Redeeming: the token minted while the permission held stops working once it is gone.
	allowed = true
	token, err := service.NewActionToken("alice", "job-1", "interaction-1", []string{"allow"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	allowed = false
	if _, err := service.Act(token, "allow"); !errors.Is(err, ErrActionForbidden) {
		t.Fatalf("act after the grant was revoked = %v, want ErrActionForbidden", err)
	}

	// And a service wired without the permission check hands out nothing (fail closed).
	bare, err := NewService(Options{Store: store, Jobs: &dispatchJobs{}, ConfigDir: t.TempDir(), UserCallers: func() []string { return []string{"alice"} }})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer bare.Close()
	if _, err := bare.NewActionToken("alice", "job-1", "interaction-1", []string{"allow"}); !errors.Is(err, ErrActionForbidden) {
		t.Fatalf("mint on a service without MayAct = %v, want ErrActionForbidden", err)
	}
}
