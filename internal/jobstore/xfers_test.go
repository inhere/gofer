package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestXferRoundTripAndList: the XFER-01 journal row survives a round trip, the
// listing honours the state/runner filters (newest first) and the expiry query
// returns exactly the rows past their TTL that are not already expired.
func TestXferRoundTripAndList(t *testing.T) {
	s := openTest(t)

	put := XferRecord{
		ID: "xf-1", Op: "put", Runner: "local", ProjectKey: "demo", Path: "tmp/in/a.bin",
		Size: 1234, SHA256: "abc", State: "staged", CallerID: "alice", Force: 1,
		CreatedAt: 100, ExpiresAt: 200,
	}
	get := XferRecord{
		ID: "xf-2", Op: "get", Runner: "w-1", ProjectKey: "demo", Path: "tmp/out/b.csv",
		State: "done", CallerID: "alice", CreatedAt: 300, FinishedAt: 301, ExpiresAt: 400,
	}
	assert.NoErr(t, s.InsertXfer(put))
	assert.NoErr(t, s.InsertXfer(get))

	got, ok, err := s.GetXfer("xf-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if got.Op != "put" || got.Runner != "local" || got.ProjectKey != "demo" || got.Path != "tmp/in/a.bin" {
		t.Fatalf("row = %+v, want the inserted put", got)
	}
	if got.Size != 1234 || got.SHA256 != "abc" || got.Force != 1 || got.CallerID != "alice" {
		t.Fatalf("row = %+v, want size/sha/force/caller preserved", got)
	}
	if got.FinishedAt != 0 || got.JobID != "" {
		t.Fatalf("row = %+v, want empty finished_at/job_id", got)
	}

	if _, ok, _ := s.GetXfer("nope"); ok {
		t.Fatalf("GetXfer(nope) reported ok")
	}

	all, err := s.ListXfers(XferFilter{})
	assert.NoErr(t, err)
	if len(all) != 2 || all[0].ID != "xf-2" {
		t.Fatalf("list = %+v, want both rows newest-first", all)
	}
	byState, err := s.ListXfers(XferFilter{State: "staged"})
	assert.NoErr(t, err)
	if len(byState) != 1 || byState[0].ID != "xf-1" {
		t.Fatalf("state filter = %+v, want only xf-1", byState)
	}
	byRunner, err := s.ListXfers(XferFilter{Runner: "w-1"})
	assert.NoErr(t, err)
	if len(byRunner) != 1 || byRunner[0].ID != "xf-2" {
		t.Fatalf("runner filter = %+v, want only xf-2", byRunner)
	}

	// The worker's upload records the payload it produced.
	assert.NoErr(t, s.SetXferContent("xf-2", 42, "def"))
	got, _, _ = s.GetXfer("xf-2")
	if got.Size != 42 || got.SHA256 != "def" {
		t.Fatalf("content = %d/%s, want 42/def", got.Size, got.SHA256)
	}

	// State moves keep the caller-supplied error text and completion time.
	assert.NoErr(t, s.UpdateXferState("xf-1", "failed", "exists", 150))
	got, _, _ = s.GetXfer("xf-1")
	if got.State != "failed" || got.Error != "exists" || got.FinishedAt != 150 {
		t.Fatalf("row = %+v, want failed/exists/150", got)
	}
	// A state move with no reason/time clears nothing.
	assert.NoErr(t, s.UpdateXferState("xf-2", "expired", "", 0))
	got, _, _ = s.GetXfer("xf-2")
	if got.State != "expired" || got.FinishedAt != 301 {
		t.Fatalf("row = %+v, want expired and finished_at preserved", got)
	}

	// Expiry: only rows past their TTL and not already expired.
	assert.NoErr(t, s.UpdateXferState("xf-1", "staged", "", 0))
	due, err := s.ExpiredXfers(400)
	assert.NoErr(t, err)
	if len(due) != 1 || due[0].ID != "xf-1" {
		t.Fatalf("expired = %+v, want only xf-1 (xf-2 is already expired)", due)
	}
	due, err = s.ExpiredXfers(100)
	assert.NoErr(t, err)
	if len(due) != 0 {
		t.Fatalf("expired at t=100 = %+v, want none", due)
	}

	assert.NoErr(t, s.DeleteXfer("xf-1"))
	if _, ok, _ := s.GetXfer("xf-1"); ok {
		t.Fatalf("row survived DeleteXfer")
	}

	// A half-built row is refused rather than stored.
	if err := s.InsertXfer(XferRecord{ID: "xf-3", Op: "put"}); err == nil {
		t.Fatalf("InsertXfer accepted a row with no runner/project/path/state")
	}
}
