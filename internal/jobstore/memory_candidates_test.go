package jobstore

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestMemoryCandidatesStore(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memcand.db"))
	assert.NoErr(t, err)
	defer s.Close()

	_, err = s.AddMemoryCandidates(" ", "p", []string{"x"})
	assert.True(t, errors.Is(err, ErrWorkInvalid))

	n, err := s.AddMemoryCandidates("job-1", "p", []string{" a ", "", "b", "a"})
	assert.NoErr(t, err)
	assert.Eq(t, 2, n)
	n, err = s.AddMemoryCandidates("job-1", "p", []string{"a", "b"}) // the same job again: idempotent
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
	n, err = s.AddMemoryCandidates("job-2", "q", []string{"a"}) // the same text from another job is its own row
	assert.NoErr(t, err)
	assert.Eq(t, 1, n)

	rows, err := s.ListMemoryCandidates(MemoryCandidateFilter{JobID: "job-1"})
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(rows))
	assert.Eq(t, "a", rows[0].Text)
	assert.Eq(t, MemoryCandidatePending, rows[0].Status)

	// Claim, then undo (a failed memory write puts the candidate back).
	got, err := s.DecideMemoryCandidate(rows[0].ID, MemoryCandidatePending, MemoryCandidateAccepted, "k", "alice")
	assert.NoErr(t, err)
	assert.Eq(t, "k", got.MemoryKey)
	assert.True(t, got.DecidedAt > 0)
	_, err = s.DecideMemoryCandidate(rows[0].ID, MemoryCandidatePending, MemoryCandidateRejected, "", "bob")
	assert.True(t, errors.Is(err, ErrMemoryCandidateDecided))
	got, err = s.DecideMemoryCandidate(rows[0].ID, MemoryCandidateAccepted, MemoryCandidatePending, "", "")
	assert.NoErr(t, err)
	assert.Eq(t, MemoryCandidatePending, got.Status)
	assert.Eq(t, "", got.MemoryKey)
	assert.Eq(t, int64(0), got.DecidedAt)

	_, err = s.DecideMemoryCandidate(424242, MemoryCandidatePending, MemoryCandidateRejected, "", "bob")
	assert.True(t, errors.Is(err, ErrMemoryCandidateNotFound))
	_, err = s.DecideMemoryCandidate(rows[0].ID, MemoryCandidatePending, "bogus", "", "bob")
	assert.True(t, errors.Is(err, ErrWorkInvalid))

	q, err := s.ListMemoryCandidates(MemoryCandidateFilter{ProjectKey: "q", Status: "all"})
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(q))
}
