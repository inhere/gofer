package tracker

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func autoArchiveFixture(t *testing.T) (*Store, string, time.Time) {
	t.Helper()
	root := t.TempDir()
	s, _, err := Init(root, "aa", true)
	assert.Require(t, assert.NoErr(t, err))
	gitIn(t, root, "init")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-m", "base")
	return s, root, time.Now().UTC()
}

func putMemories(t *testing.T, s *Store, items ...Memory) {
	t.Helper()
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]Memory) ([]Memory, error) { return items, nil })))
}

func ago(now time.Time, d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }

func liveKeys(t *testing.T, s *Store) []string {
	t.Helper()
	items, err := s.ReadMemories()
	assert.Require(t, assert.NoErr(t, err))
	var keys []string
	for _, m := range items {
		keys = append(keys, m.Key)
	}
	return keys
}

const day = 24 * time.Hour

func TestAutoArchiveHandoffRule(t *testing.T) {
	s, _, now := autoArchiveFixture(t)
	handoff := func(key string, updated, expires time.Duration) Memory {
		return Memory{Key: key, Content: "state", UpdatedAt: ago(now, updated), MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff, ExpiresAt: ago(now, expires)}}
	}
	putMemories(t, s,
		handoff("h-long-gone", 30*day, 10*day),
		handoff("h-just-expired", 20*day, 3*day), // expired, but inside the 7-day grace
		handoff("h-alive", 1*day, -5*day),
		handoff("h-touched", 1*day, 10*day), // expired long ago but changed yesterday
		Memory{Key: "h-default-ttl", Content: "x", UpdatedAt: ago(now, 40*day), MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}}, // updated + 14d = 26d ago
		Memory{Key: "r-expired", Content: "rule", UpdatedAt: ago(now, 40*day), MemoryMeta: MemoryMeta{Kind: MemoryKindRule, ExpiresAt: ago(now, 30*day)}},
	)
	got, err := s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	var keys []string
	for _, a := range got {
		keys = append(keys, a.Key)
		assert.Contains(t, a.Reason, "handoff expired")
	}
	assert.Eq(t, []string{"h-default-ttl", "h-long-gone"}, keys)
	assert.Eq(t, []string{"h-alive", "h-just-expired", "h-touched", "r-expired"}, liveKeys(t, s))

	archived, err := s.ReadArchivedMemories()
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, archived, 2)
	for _, a := range archived {
		assert.Eq(t, AutoArchiveActor, a.ArchivedBy)
		assert.NotEmpty(t, a.ArchivedAt)
	}

	// Idempotent: a second run has nothing left to do.
	again, err := s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, again, 0)
}

func TestAutoArchiveNoteRule(t *testing.T) {
	s, root, now := autoArchiveFixture(t)
	// The full hash: a 10-char short hash is all digits about 1% of the time, and
	// looksLikeCommit (digits AND a-f letters) then does not see it as a commit — the note
	// would read as paths-only and be archived (seen once on a CI runner).
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	assert.Require(t, assert.NoErr(t, err))
	realCommit := strings.TrimSpace(string(head))
	assert.True(t, looksLikeCommit(realCommit), realCommit)
	old := ago(now, 30*day)
	note := func(key, content string) Memory { return Memory{Key: key, Content: content, UpdatedAt: old} }
	putMemories(t, s,
		note("n-all-gone", "see `docs/gone-a.md` and `docs/gone-b.md`, fixed in 0badc0ffee1"),
		note("n-paths-only", "see `docs/gone-c.md`"),
		note("n-one-path-lives", "see `docs/gone-d.md` and `.gofer/tracker/config.yaml`"),
		note("n-commit-lives", "see `docs/gone-e.md`, built at "+realCommit),
		note("n-commits-only", "fixed in 0badc0ffee1"),
		note("n-no-refs", "plain prose"),
		Memory{Key: "n-touched", Content: "see `docs/gone-f.md`", UpdatedAt: ago(now, 2*day)},
		Memory{Key: "n-ignored", Content: "see `docs/gone-g.md`", UpdatedAt: old, MemoryMeta: MemoryMeta{DoctorIgnore: []string{DoctorPathMissing}}},
		Memory{Key: "r-all-gone", Content: "see `docs/gone-h.md`", UpdatedAt: old, MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
		Memory{Key: "r-tag-prime", Content: "see `docs/gone-i.md`", UpdatedAt: old, Tags: []string{"prime"}}, // the legacy prime tag is a rule
		Memory{Key: "h-gone-paths", Content: "see `docs/gone-j.md`", UpdatedAt: ago(now, 3*day), MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}},
	)
	got, err := s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	var keys []string
	for _, a := range got {
		keys = append(keys, a.Key)
		assert.Eq(t, MemoryKindNote, a.Kind)
		assert.Contains(t, a.Reason, "every referenced path and commit is gone")
	}
	assert.Eq(t, []string{"n-all-gone", "n-paths-only"}, keys)
	live := liveKeys(t, s)
	for _, k := range []string{"r-all-gone", "r-tag-prime", "n-one-path-lives", "n-commit-lives", "n-commits-only", "n-no-refs", "n-touched", "n-ignored", "h-gone-paths"} {
		assert.True(t, hasTag(live, k), k)
	}
}

func TestAutoArchiveNoteSuppressedAndSwitchedOff(t *testing.T) {
	s, _, now := autoArchiveFixture(t)
	putMemories(t, s, Memory{Key: "n-gone", Content: "see `docs/gone.md`", UpdatedAt: ago(now, 30*day)})

	assert.Require(t, assert.NoErr(t, s.UpdateConfig(func(c *Config) {
		c.Prime.Doctor = &PrimeDoctorConfig{Suppress: []string{DoctorPathMissing}}
	})))
	got, err := s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, got, 0)

	off := false
	assert.Require(t, assert.NoErr(t, s.UpdateConfig(func(c *Config) { c.Prime.Doctor = &PrimeDoctorConfig{AutoArchive: &off} })))
	assert.False(t, mustCfg(t, s).Prime.AutoArchiveEnabled())
	got, err = s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, got, 0)
	assert.Eq(t, []string{"n-gone"}, liveKeys(t, s))

	// Back on (the default when the key is absent): the note goes.
	assert.Require(t, assert.NoErr(t, s.UpdateConfig(func(c *Config) { c.Prime.Doctor = nil })))
	assert.True(t, mustCfg(t, s).Prime.AutoArchiveEnabled())
	got, err = s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, got, 1)
}

func mustCfg(t *testing.T, s *Store) Config {
	t.Helper()
	cfg, err := s.ReadConfig()
	assert.Require(t, assert.NoErr(t, err))
	return cfg
}

// A store outside a repository (the global memories) has no path check: its notes stay,
// while an expired handoff is still archived.
func TestAutoArchiveGlobalStoreSkipsNotes(t *testing.T) {
	s, err := OpenGlobal(filepath.Join(t.TempDir(), "tracker"))
	assert.Require(t, assert.NoErr(t, err))
	now := time.Now().UTC()
	putMemories(t, s,
		Memory{Key: "n-gone", Content: "see `docs/gone.md`", UpdatedAt: ago(now, 60*day)},
		Memory{Key: "h-old", Content: "x", UpdatedAt: ago(now, 60*day), MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}},
	)
	got, err := s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, got, 1)
	assert.Eq(t, "h-old", got[0].Key)
	assert.Eq(t, []string{"n-gone"}, liveKeys(t, s))
}

func TestAutoArchiveRestoreAndDoctorListing(t *testing.T) {
	s, _, now := autoArchiveFixture(t)
	putMemories(t, s, Memory{Key: "h-old", Content: "x", UpdatedAt: ago(now, 60*day), MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}})
	_, err := s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))

	r, err := s.Doctor(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, r.AutoArchived, 1)
	assert.Eq(t, "h-old", r.AutoArchived[0].Key)
	out := FormatDoctorReportIn("en", r)
	assert.Contains(t, out, "auto-archived")
	assert.Contains(t, out, "h-old [handoff]")

	// A manual archive is not listed as automatic.
	putMemories(t, s, Memory{Key: "manual", Content: "m", UpdatedAt: Now()})
	_, err = s.ArchiveMemory("manual", "by hand", "me")
	assert.Require(t, assert.NoErr(t, err))
	r, _ = s.Doctor(now)
	assert.Len(t, r.AutoArchived, 1)

	// Restore finds it again, and the restored memory is not archived straight away
	// (updated_at is now, inside the grace period).
	restored, err := s.RestoreMemory("h-old", "me")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "x", restored.Content)
	again, err := s.AutoArchiveMemories(time.Now().UTC())
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, again, 0)
	assert.True(t, hasTag(liveKeys(t, s), "h-old"))
	r, _ = s.Doctor(time.Now().UTC())
	assert.Len(t, r.AutoArchived, 0)

	// Listed for 30 days only.
	putMemories(t, s, Memory{Key: "h-old2", Content: "x", UpdatedAt: ago(now, 60*day), MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}})
	_, err = s.AutoArchiveMemories(now)
	assert.Require(t, assert.NoErr(t, err))
	r, _ = s.Doctor(now.Add(31 * day))
	assert.Len(t, r.AutoArchived, 0)
}
