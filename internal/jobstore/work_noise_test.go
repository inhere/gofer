package jobstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func noiseStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "n.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestIncrementSessionHumanPrompts(t *testing.T) {
	s := noiseStore(t)
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "s1", Agent: "claude"})
	assert.NoErr(t, err)

	n, err := s.IncrementSessionHumanPrompts("s1")
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)
	n, err = s.IncrementSessionHumanPrompts("s1")
	assert.NoErr(t, err)
	assert.Eq(t, 2, n)

	a, ok, err := s.GetAgentSession("s1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 2, a.HumanPrompts)

	// An unknown session is not an error: the beat raced a delete.
	n, err = s.IncrementSessionHumanPrompts("nope")
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
}

func TestShortSession(t *testing.T) {
	base := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC).Unix()
	ended := AgentSession{State: SessionEnded, StartedAt: base, EndedAt: base + 4*60}

	cases := []struct {
		name       string
		a          AgentSession
		maxMinutes int
		maxPrompts int
		short      bool
	}{
		{name: "short and few prompts", a: ended, maxMinutes: 10, maxPrompts: 3, short: true},
		{name: "long enough", a: AgentSession{State: SessionEnded, StartedAt: base, EndedAt: base + 10*60}, maxMinutes: 10, maxPrompts: 3},
		{name: "many prompts", a: func() AgentSession { x := ended; x.HumanPrompts = 3; return x }(), maxMinutes: 10, maxPrompts: 3},
		{name: "still running", a: AgentSession{State: SessionRunning, StartedAt: base, LastSeenAt: base + 60}, maxMinutes: 10, maxPrompts: 3},
		{name: "offline without ended_at uses last seen", a: AgentSession{State: SessionOffline, StartedAt: base, LastSeenAt: base + 60}, maxMinutes: 10, maxPrompts: 3, short: true},
		{name: "minutes threshold off", a: ended, maxMinutes: 0, maxPrompts: 3},
		{name: "prompts threshold off", a: ended, maxMinutes: 10, maxPrompts: 0},
		{name: "no start time", a: AgentSession{State: SessionEnded, EndedAt: base + 60}, maxMinutes: 10, maxPrompts: 3},
	}
	for _, c := range cases {
		reason, short := ShortSession(c.a, c.maxMinutes, c.maxPrompts, base+3600)
		assert.Eq(t, c.short, short, c.name)
		if c.short {
			assert.True(t, reason != "", c.name)
		} else {
			assert.Eq(t, "", reason, c.name)
		}
	}
}

func TestSkippedSummaryDoesNotCountAgainstTheDailyCap(t *testing.T) {
	s := noiseStore(t)
	w, err := s.CreateWorkItem(WorkItemInput{Title: "t", By: "human:a"})
	assert.NoErr(t, err)
	now := s.unixNow()

	id, err := s.BeginWorkSummary(w.ID, "s1", now, "auto")
	assert.NoErr(t, err)
	assert.NoErr(t, s.FinishWorkSummary(id, SummarySkipped, "", "短命会话"))
	id, err = s.BeginWorkSummary(w.ID, "s1", now, "auto")
	assert.NoErr(t, err)
	assert.NoErr(t, s.FinishWorkSummary(id, SummaryOK, "", ""))

	n, err := s.CountAutoWorkSummaries(now - 60)
	assert.NoErr(t, err)
	assert.Eq(t, 1, n) // the skipped one is free
}
