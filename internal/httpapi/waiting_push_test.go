package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// The `waiting` push topic (waiting_push.go): the 「等我」 list as a snapshot, notified when its
// set changes, filtered by a connection's project restriction, and never carrying the text of a
// turn.

// newWaitingPushServer builds a server with the waiting snapshot wired into a hub whose
// coalescing interval is short enough for a test to observe each notification. The server's own
// hub is replaced by that one, so nothing else pushes on it.
func newWaitingPushServer(t *testing.T) (*Server, *pushhub.Hub) {
	t.Helper()
	s := recordingServerWith(t, config.ServerConfig{
		WebBaseURL: "https://gofer.example.com",
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{HostPath: cfg.Storage.Root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true}
		}
	})
	h := pushhub.New(pushhub.Options{WaitingInterval: 20 * time.Millisecond})
	h.SetSnapshot(pushhub.TopicWaiting, s.waitingListSnapshot)
	h.SetScopedSnapshot(pushhub.TopicWaiting, func(scope func(string) bool) (any, error) {
		return s.waitingListSnapshotFor(scope)
	})
	s.live = h
	return s, h
}

// nextWaitingSnapshot reads frames until one `waiting` snapshot arrives (others are skipped) and
// returns its rows by subject key → category.
func nextWaitingSnapshot(t *testing.T, c *pushhub.Conn) map[string]string {
	t.Helper()
	var got map[string]string
	wait.Until(t, 10*time.Second, "a waiting snapshot frame", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for {
			b, err := c.Next(ctx)
			if err != nil {
				return false
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil || m["t"] != "snap" || m["topic"] != pushhub.TopicWaiting {
				continue
			}
			list, _ := m["data"].([]any)
			got = make(map[string]string, len(list))
			for _, it := range list {
				e, _ := it.(map[string]any)
				key, _ := e["key"].(string)
				cat, _ := e["category"].(string)
				got[key] = cat
			}
			return true
		}
	})
	return got
}

// pendingWaitingFrames drains what the hub still holds and reports the waiting snapshots among
// it (used to prove that nothing was pushed).
func pendingWaitingFrames(t *testing.T, c *pushhub.Conn) int {
	t.Helper()
	n := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		b, err := c.Next(ctx)
		cancel()
		if err != nil {
			return n
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil && m["t"] == "snap" && m["topic"] == pushhub.TopicWaiting {
			n++
		}
	}
}

// An unchanged set is not pushed again: the sweep notifies only when the list really changed.
func TestWaitingTopicNotifiesOnlyOnChange(t *testing.T) {
	s, h := newWaitingPushServer(t)
	c, err := h.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.Subscribe([]string{pushhub.TopicWaiting}, nil)
	if keys := nextWaitingSnapshot(t, c); len(keys) != 0 {
		t.Fatalf("the empty list should be an empty snapshot, got %v", keys)
	}

	seedWaitingTurns(t, s)
	for i := 0; i < 3; i++ {
		s.sweepWaitingNotices()
	}
	keys := nextWaitingSnapshot(t, c)
	if len(keys) != 2 || keys["session:sess-p"] != "answer" || keys["job:acp-marked"] != "answer" {
		t.Fatalf("snapshot after the entry = %v", keys)
	}

	// Three more passes judged the same set: the coalescer interval has passed, so a poke would
	// have produced a frame by now.
	time.Sleep(100 * time.Millisecond)
	if n := pendingWaitingFrames(t, c); n != 0 {
		t.Fatalf("the settled set produced %d extra frames", n)
	}
}

// Leaving the list notifies once; a subject coming back is a new change.
func TestWaitingTopicLeavingNotifies(t *testing.T) {
	s, h := newWaitingPushServer(t)
	c, err := h.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.Subscribe([]string{pushhub.TopicWaiting}, nil)
	nextWaitingSnapshot(t, c) // the empty list

	seedWaitingTurns(t, s)
	s.sweepWaitingNotices()
	if keys := nextWaitingSnapshot(t, c); len(keys) != 2 {
		t.Fatalf("entered snapshot = %v", keys)
	}

	// Answering the terminal turn ends its stay.
	if _, err := s.jobs.Meta().ExpireSessionDecisions("sess-p"); err != nil {
		t.Fatal(err)
	}
	s.sweepWaitingNotices()
	keys := nextWaitingSnapshot(t, c)
	if _, still := keys["session:sess-p"]; still || len(keys) != 1 {
		t.Fatalf("after leaving = %v, want only the ACP job", keys)
	}

	// The same set again changes nothing.
	s.sweepWaitingNotices()
	time.Sleep(100 * time.Millisecond)
	if n := pendingWaitingFrames(t, c); n != 0 {
		t.Fatalf("the settled set produced %d extra frames", n)
	}
}

// A connection under a project restriction only sees the rows of its own projects.
func TestWaitingTopicScopeSeesOwnProjectsOnly(t *testing.T) {
	s, h := newWaitingPushServer(t)
	seedWaitingTurns(t, s)
	now := time.Now().Unix()
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "acp-p2", ProjectKey: "p2", Agent: "claude-acp", Runner: "local", Status: "awaiting_input",
		Cwd: ".", RequestJSON: `{"title":"别的项目"}`, SessionStateJSON: `{"turn_no":1}`, CallerID: "acp-owner", StartedAt: now - 7200, UpdatedAt: now - 3600}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.jobs.Meta().InsertJobEvent(jobstore.JobEvent{JobID: "acp-p2", Type: "job.awaiting_input",
		Detail: `{"turn_no":1,"needs_reply":true}`, At: now - 3600}); err != nil {
		t.Fatal(err)
	}

	scoped, err := h.RegisterScoped("lim", pushhub.ConnScope{Key: "p1", Scope: func(p string) bool { return p == "p1" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scoped.Close)
	scoped.Subscribe([]string{pushhub.TopicWaiting}, nil)
	keys := nextWaitingSnapshot(t, scoped)
	if _, ok := keys["job:acp-p2"]; ok {
		t.Fatalf("a restricted connection saw another project's row: %v", keys)
	}
	if keys["session:sess-p"] == "" || keys["job:acp-marked"] == "" {
		t.Fatalf("a restricted connection lost its own rows: %v", keys)
	}
}

// The snapshot says what waits, never the text of the turn behind it.
func TestWaitingTopicCarriesNoTurnText(t *testing.T) {
	s, h := newWaitingPushServer(t)
	c, err := h.Register("alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.Subscribe([]string{pushhub.TopicWaiting}, nil)
	nextWaitingSnapshot(t, c)
	seedWaitingTurns(t, s)

	payload, err := s.waitingListSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), turnText) {
		t.Fatalf("the waiting snapshot carries the turn text: %s", raw)
	}
	rows := payload.([]waitingView)
	if len(rows) == 0 {
		t.Fatal("nothing waits for a person")
	}
	for _, r := range rows {
		if r.Title == "" || r.Key == "" || r.Category == "" {
			t.Fatalf("a row lacks what identifies it: %+v", r)
		}
	}
}
