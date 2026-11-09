package jobstore

import (
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func insertRelayTurn(t *testing.T, s *Store, id, sid string, askedAt, timeout int64, needsReply bool) {
	t.Helper()
	d := PlanDecision{ID: id, Title: "t", Question: "q", SessionID: sid, Kind: DecisionKindRelay,
		TimeoutSec: timeout, AskedAt: askedAt, NeedsReply: needsReply}
	assert.NoErr(t, s.InsertDecision(&d))
}

func TestDecisionReleasedAt(t *testing.T) {
	s := openTest(t)
	now := time.Now().Unix()
	fixed := time.Unix(now+10000, 0)
	s.SetClock(func() time.Time { return fixed })

	// Expiry path: released_at is the deadline, not the (later) scan time.
	insertRelayTurn(t, s, "d-exp", "s1", now, 60, false)
	got, ok, err := s.GetDecision("d-exp")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, DecisionExpired, got.State)
	assert.Eq(t, now+60, got.ReleasedAt)

	// Release path.
	s.SetClock(func() time.Time { return time.Unix(now, 0) })
	insertRelayTurn(t, s, "d-rel", "s2", now, 600, true)
	okRel, err := s.ReleaseDecision("d-rel", "user_returned")
	assert.NoErr(t, err)
	assert.True(t, okRel)
	got, _, _ = s.GetDecision("d-rel")
	assert.Eq(t, now, got.ReleasedAt)

	// Session expiry path.
	insertRelayTurn(t, s, "d-sess", "s3", now, 600, false)
	n, err := s.ExpireSessionDecisions("s3")
	assert.NoErr(t, err)
	assert.Eq(t, int64(1), n)
	got, _, _ = s.GetDecision("d-sess")
	assert.Eq(t, now, got.ReleasedAt)

	// Answer path leaves released_at at 0.
	insertRelayTurn(t, s, "d-ans", "s4", now, 600, false)
	okAns, err := s.AnswerDecision("d-ans", "yes", "me")
	assert.NoErr(t, err)
	assert.True(t, okAns)
	got, _, _ = s.GetDecision("d-ans")
	assert.Eq(t, int64(0), got.ReleasedAt)
}

func TestDecisionNeedsReplyRoundTrip(t *testing.T) {
	s := openTest(t)
	now := time.Now().Unix()
	insertRelayTurn(t, s, "d-yes", "s1", now, 600, true)
	insertRelayTurn(t, s, "d-no", "s1", now, 600, false)
	got, _, _ := s.GetDecision("d-yes")
	assert.True(t, got.NeedsReply)
	got, _, _ = s.GetDecision("d-no")
	assert.False(t, got.NeedsReply)

	// An old row (column default, never written) reads as false.
	_, err := s.db.Exec(`INSERT INTO plan_decisions (id, title, question, state, timeout_sec, asked_at)
  VALUES ('d-old','t','q','OPEN',600,?)`, now)
	assert.NoErr(t, err)
	got, _, _ = s.GetDecision("d-old")
	assert.False(t, got.NeedsReply)
	assert.Eq(t, int64(0), got.ReleasedAt)
}
