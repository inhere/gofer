package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/inhere/gofer/internal/tracker"
)

// The tracker mirror stores memory bodies opaquely: kind / summary / when /
// expires_at / source / created_at survive a push and a pull unchanged.
func TestSyncRoundTripsMemoryMeta(t *testing.T) {
	t.Parallel()
	e := newTrackerE2E(t)
	kind, summary, source := "handoff", "交接一句话", "plan:p1"
	kw := []string{"发版"}
	sent, err := e.local.SetMemoryPatch("h", tracker.MemoryPatch{Content: "body", Kind: &kind, Summary: &summary, Source: &source, WhenKeywords: &kw, By: "me"}, true)
	if err != nil {
		t.Fatal(err)
	}
	syncTracker(t, e)
	recs, err := e.meta.ListTrackerMemories(mustConfig(t, e.local).TrackerID, 0)
	if err != nil || len(recs) != 1 {
		t.Fatalf("server records: %+v %v", recs, err)
	}
	var stored tracker.Memory
	if err := json.Unmarshal(recs[0].Body, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Kind != kind || stored.Summary != summary || stored.Source != source || stored.ExpiresAt != sent.ExpiresAt || stored.CreatedAt != sent.CreatedAt || stored.When == nil || stored.When.Keywords[0] != "发版" {
		t.Fatalf("server body lost fields: %s", recs[0].Body)
	}
	syncTracker(t, e)
	got, err := e.local.Memory("h")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(sent)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatalf("local after pull:\n%s\n%s", a, b)
	}
}
