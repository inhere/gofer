package bgagent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func seedSession(t *testing.T, f *fixture, kind, project, sid string, used, limit int64) {
	t.Helper()
	err := f.store.ReplaceBackgroundSession(jobstore.BackgroundSession{
		Kind: kind, SessionID: sid, Agent: "a1", Project: project, Model: "m1",
		CreatedAt: clockT0.Unix(), LastUsedAt: clockT0.Unix(), Runs: 2,
		ContextUsed: used, ContextLimit: limit, State: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestViewEmptyAndUnknownContext: no session is an empty list (never null, never a made-up
// session) and an unreported context is null in JSON, not 0.
func TestViewEmptyAndUnknownContext(t *testing.T) {
	f := newFixture(t)
	seedSession(t, f, KindSteward, "p1", "s-known", 50, 100)
	seedSession(t, f, KindSummarizer, "p1", "s-unknown", 0, 0)

	var all []KindStatus
	for _, k := range Kinds() {
		st, err := f.r.Status(k, Policy{MaxRuns: 20})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, st)
	}
	raw, err := json.Marshal(NewView(all...))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Version int `json:"version"`
		Kinds   []struct {
			Kind     string `json:"kind"`
			Sessions []map[string]any
		} `json:"kinds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != ViewVersion || len(got.Kinds) != 3 {
		t.Fatalf("view = %s", raw)
	}
	by := map[string][]map[string]any{}
	for _, k := range got.Kinds {
		by[k.Kind] = k.Sessions
	}
	if by[KindJobCheck] == nil || len(by[KindJobCheck]) != 0 {
		t.Fatalf("job_check sessions = %#v, want an empty (non-null) list", by[KindJobCheck])
	}
	if !strings.Contains(string(raw), `"job_check","sessions":[]`) {
		t.Fatalf("raw view does not carry an empty sessions array: %s", raw)
	}
	unk := by[KindSummarizer][0]
	for _, f := range []string{"context_used_tokens", "context_limit_tokens", "context_ratio", "last_usage"} {
		if v, ok := unk[f]; !ok || v != nil {
			t.Errorf("unknown-context session %s = %#v (present %v), want null", f, v, ok)
		}
	}
	known := by[KindSteward][0]
	if known["context_ratio"] != 0.5 || known["context_used_tokens"] != float64(50) || known["context_limit_tokens"] != float64(100) {
		t.Errorf("known-context session = %#v", known)
	}
}

// TestResetKindEndsEveryScope: ResetKind ends the active sessions of all projects of one kind,
// leaves other kinds alone and audits each scope.
func TestResetKindEndsEveryScope(t *testing.T) {
	f := newFixture(t)
	seedSession(t, f, KindSummarizer, "p1", "s1", 0, 0)
	seedSession(t, f, KindSummarizer, "p2", "s2", 0, 0)
	seedSession(t, f, KindSteward, "p1", "s3", 0, 0)

	n, err := f.r.ResetKind(KindSummarizer, "alice")
	if err != nil || n != 2 {
		t.Fatalf("ResetKind = %d, %v; want 2", n, err)
	}
	st, _ := f.r.Status(KindSummarizer, Policy{})
	if len(st.Sessions) != 0 {
		t.Fatalf("summarizer sessions after reset = %d", len(st.Sessions))
	}
	other, _ := f.r.Status(KindSteward, Policy{})
	if len(other.Sessions) != 1 {
		t.Fatalf("steward sessions = %d, want 1 (other kinds are untouched)", len(other.Sessions))
	}
	if n, err := f.r.ResetKind(KindSummarizer, "alice"); err != nil || n != 0 {
		t.Fatalf("second ResetKind = %d, %v; want 0", n, err)
	}
	evs, err := f.store.ListAuditEvents("background_agent.reset", "summarizer:p1")
	if err != nil || len(evs) != 1 {
		t.Fatalf("audit events = %d, %v; want 1", len(evs), err)
	}
}

func TestKnownKind(t *testing.T) {
	for _, k := range Kinds() {
		if !KnownKind(k) {
			t.Errorf("%s not known", k)
		}
	}
	if KnownKind("") || KnownKind("nope") {
		t.Error("unknown kind accepted")
	}
}
