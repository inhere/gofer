package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestWakeupRoundTripDueAndAdvance: a wakeup row survives a round trip with its JSON
// filters decoded, the due query returns exactly the enabled rows that came due (or
// passed their TTL), the advance compare-and-swap only wins on the value it read, and
// the fire bookkeeping (claim → continuation → release) moves the slot exactly once.
func TestWakeupRoundTripDueAndAdvance(t *testing.T) {
	s := openTest(t)

	at := WakeupRecord{
		ID: "wk-1", JobID: "job-1", Kind: WakeupKindAt, At: 1000,
		Mode: WakeupModeOnce, Instruction: "check the build", Enabled: 1,
		NextRunAt: 1000, CreatedBy: "alice", CreatedAt: 100, ExpiresAt: 900_000,
	}
	event := WakeupRecord{
		ID: "wk-2", JobID: "job-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal", "job.stalled"}),
		FilterJobID:    "job-2", FilterStatusJSON: EncodeStringList([]string{"done"}),
		Mode: WakeupModeContinuous, Enabled: 1, CreatedAt: 200, ExpiresAt: 900_000,
	}
	assert.NoErr(t, s.InsertWakeup(at))
	assert.NoErr(t, s.InsertWakeup(event))

	got, ok, err := s.GetWakeup("wk-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if got.Kind != WakeupKindAt || got.At != 1000 || got.NextRunAt != 1000 || got.Instruction != "check the build" {
		t.Fatalf("row = %+v, want the inserted timer", got)
	}
	if got.Revision != 1 || got.FiredCount != 0 || got.CoalescedCount != 0 || got.ContinuationJobID != "" {
		t.Fatalf("row = %+v, want fresh fire bookkeeping", got)
	}
	if _, ok, _ := s.GetWakeup("nope"); ok {
		t.Fatalf("GetWakeup(nope) reported ok")
	}

	got, _, _ = s.GetWakeup("wk-2")
	if types := DecodeStringList(got.EventTypesJSON); len(types) != 2 || types[0] != "job.terminal" {
		t.Fatalf("event types = %v, want the stored pair", types)
	}
	if st := DecodeStringList(got.FilterStatusJSON); len(st) != 1 || st[0] != "done" {
		t.Fatalf("filter status = %v, want [done]", st)
	}

	// The listing is per job, oldest first.
	rows, err := s.ListWakeups("job-1")
	assert.NoErr(t, err)
	if len(rows) != 2 || rows[0].ID != "wk-1" || rows[1].ID != "wk-2" {
		t.Fatalf("list = %+v, want both rows oldest-first", rows)
	}
	if other, err := s.ListWakeups("job-9"); err != nil || len(other) != 0 {
		t.Fatalf("list(other) = %+v/%v, want empty", other, err)
	}

	// Due: nothing at t=999, the timer at t=1000, and only the timer — an event
	// wakeup has no instant to come due.
	if due, err := s.DueWakeups(999); err != nil || len(due) != 0 {
		t.Fatalf("due(999) = %+v/%v, want none", due, err)
	}
	due, err := s.DueWakeups(1000)
	assert.NoErr(t, err)
	if len(due) != 1 || due[0].ID != "wk-1" {
		t.Fatalf("due(1000) = %+v, want only the timer", due)
	}

	// The advance CAS is won once and only once for the value that was read.
	ok, err = s.AdvanceWakeup("wk-1", 1000, 2000, 1000)
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, err = s.AdvanceWakeup("wk-1", 1000, 3000, 1000)
	assert.NoErr(t, err)
	if ok {
		t.Fatalf("the advance CAS won twice for the same old value")
	}
	got, _, _ = s.GetWakeup("wk-1")
	if got.NextRunAt != 2000 || got.LastFiredAt != 1000 {
		t.Fatalf("row = %+v, want next=2000 last_fired=1000", got)
	}

	// The TTL pass is served by the same query: a row past its expiry is due even
	// when its timer is not, which is what lets one sweep retire it.
	assert.NoErr(t, s.UpdateWakeup(WakeupRecord{
		ID: "wk-1", Kind: WakeupKindAt, At: 1000, Mode: WakeupModeOnce, Enabled: 1,
		NextRunAt: 2000, ExpiresAt: 1500, Instruction: "check the build",
	}))
	got, _, _ = s.GetWakeup("wk-1")
	if got.Revision != 2 {
		t.Fatalf("revision = %d, want the update to bump it", got.Revision)
	}
	due, err = s.DueWakeups(1600)
	assert.NoErr(t, err)
	if len(due) != 1 || due[0].ID != "wk-1" {
		t.Fatalf("due(1600) = %+v, want the expired row", due)
	}

	// A disabled row is never due, whatever its instant (the event wakeup below is
	// still inside its TTL, so this is about the switch and not about expiry).
	assert.NoErr(t, s.SetWakeupEnabled("wk-1", 0))
	if due, err := s.DueWakeups(500_000); err != nil || len(due) != 0 {
		t.Fatalf("due with the timer disabled = %+v/%v, want none", due, err)
	}

	assert.NoErr(t, s.DeleteWakeup("wk-1"))
	if _, ok, _ := s.GetWakeup("wk-1"); ok {
		t.Fatalf("row survived DeleteWakeup")
	}

	// A half-built row is refused rather than stored.
	if err := s.InsertWakeup(WakeupRecord{ID: "wk-3"}); err == nil {
		t.Fatalf("InsertWakeup accepted a row with no job_id/kind")
	}
}

// TestClaimWakeupFireIsExclusive: the continuation slot is taken exactly once. A
// second claim (a concurrent trigger, or a trigger that arrives while the submit is
// in flight) loses, which is what makes coalescing possible at all; the slot is only
// released by the value that holds it.
func TestClaimWakeupFireIsExclusive(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-1", JobID: "job-1", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "job-1", Mode: WakeupModeContinuous, Enabled: 1, CreatedAt: 1,
	}))

	ok, err := s.ClaimWakeupFire("wk-1", WakeupClaimPending)
	assert.NoErr(t, err)
	assert.True(t, ok)
	row, _, _ := s.GetWakeup("wk-1")
	if !row.IsContinuationPending() || row.FiredCount != 1 || row.LastFiredAt == 0 {
		t.Fatalf("row = %+v, want the slot claimed and counted", row)
	}

	// A second claim loses while the slot is held.
	ok, err = s.ClaimWakeupFire("wk-1", "job-2")
	assert.NoErr(t, err)
	if ok {
		t.Fatalf("a second claim won a slot that was already taken")
	}
	row, _, _ = s.GetWakeup("wk-1")
	if row.ContinuationJobID != WakeupClaimPending || row.FiredCount != 1 {
		t.Fatalf("row = %+v, want the loser to change nothing", row)
	}

	// The submit resolves the claim into the real job id.
	ok, err = s.SetWakeupContinuation("wk-1", "job-2")
	assert.NoErr(t, err)
	assert.True(t, ok)
	row, _, _ = s.GetWakeup("wk-1")
	if row.ContinuationJobID != "job-2" {
		t.Fatalf("row = %+v, want the continuation recorded", row)
	}
	// ...and only once: a fire that lost its slot cannot overwrite the winner's.
	ok, err = s.SetWakeupContinuation("wk-1", "job-3")
	assert.NoErr(t, err)
	if ok {
		t.Fatalf("SetWakeupContinuation overwrote an already-resolved slot")
	}

	// A release only clears the value it names.
	assert.NoErr(t, s.ReleaseWakeupClaim("wk-1", WakeupClaimPending))
	row, _, _ = s.GetWakeup("wk-1")
	if row.ContinuationJobID != "job-2" {
		t.Fatalf("row = %+v, want the mismatched release to be a no-op", row)
	}
	assert.NoErr(t, s.ReleaseWakeupClaim("wk-1", "job-2"))
	row, _, _ = s.GetWakeup("wk-1")
	if row.ContinuationJobID != "" {
		t.Fatalf("row = %+v, want the slot free again", row)
	}

	// A disabled wakeup cannot be claimed at all (a sweep that lost the race against
	// a disable must not fire).
	assert.NoErr(t, s.SetWakeupEnabled("wk-1", 0))
	ok, err = s.ClaimWakeupFire("wk-1", WakeupClaimPending)
	assert.NoErr(t, err)
	if ok {
		t.Fatalf("a disabled wakeup was claimed")
	}

	// Coalescing counts the triggers that did not fire.
	assert.NoErr(t, s.CoalesceWakeup("wk-1", 42))
	row, _, _ = s.GetWakeup("wk-1")
	if row.CoalescedCount != 1 || row.LastFiredAt != 42 {
		t.Fatalf("row = %+v, want one coalesce at t=42", row)
	}
}

// TestMatchingEventWakeups: the matcher query returns exactly the enabled event
// subscriptions for the source job and type — never another job's, never a timer's,
// never a type outside the subscription.
func TestMatchingEventWakeups(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-1", JobID: "job-a", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal", "job.stalled"}),
		FilterJobID:    "job-b", Enabled: 1, CreatedAt: 1,
	}))
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-2", JobID: "job-a", Kind: WakeupKindEvent,
		EventTypesJSON: EncodeStringList([]string{"job.terminal"}),
		FilterJobID:    "job-b", Enabled: 0, CreatedAt: 2, // disabled
	}))
	assert.NoErr(t, s.InsertWakeup(WakeupRecord{
		ID: "wk-3", JobID: "job-a", Kind: WakeupKindAt, At: 10, NextRunAt: 10, Enabled: 1, CreatedAt: 3,
	}))

	got, err := s.MatchingEventWakeups("job-b", "job.terminal")
	assert.NoErr(t, err)
	if len(got) != 1 || got[0].ID != "wk-1" {
		t.Fatalf("match(job-b, job.terminal) = %+v, want only wk-1", got)
	}
	if got, err := s.MatchingEventWakeups("job-b", "interaction.answered"); err != nil || len(got) != 0 {
		t.Fatalf("match of an unsubscribed type = %+v/%v, want none", got, err)
	}
	if got, err := s.MatchingEventWakeups("job-c", "job.terminal"); err != nil || len(got) != 0 {
		t.Fatalf("match of another source job = %+v/%v, want none", got, err)
	}
}
