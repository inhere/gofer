package tracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestArchiveRestoreMemory(t *testing.T) {
	s := primeTestStore(t)
	_, err := s.SetMemoryPatch("old-rule", MemoryPatch{Content: "OLD-RULE-BODY", Kind: strp(MemoryKindRule), Summary: strp("一句话"), Source: strp("issue:x-1"), By: "me"}, true)
	assert.Require(t, assert.NoErr(t, err))
	_, err = s.SetMemory("keep", "KEEP-BODY", "me")
	assert.Require(t, assert.NoErr(t, err))
	before, err := s.Memory("old-rule")
	assert.Require(t, assert.NoErr(t, err))

	a, err := s.ArchiveMemory("old-rule", "superseded", "steward")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "superseded", a.ArchiveReason)
	assert.Eq(t, "steward", a.ArchivedBy)
	_, err = s.Memory("old-rule")
	assert.Err(t, err)
	archived, err := s.ReadArchivedMemories()
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.Len(t, archived, 1))
	assert.Eq(t, before.Summary, archived[0].Summary)
	assert.Eq(t, before.Source, archived[0].Source)
	assert.Eq(t, before.Kind, archived[0].Kind)

	// Archived memories never reach prime or the job rules.
	out, err := s.Prime()
	assert.Require(t, assert.NoErr(t, err))
	assert.NotContains(t, out, "OLD-RULE-BODY")
	assert.NotContains(t, out, "old-rule")
	rule, err := s.PrimeRule()
	assert.Require(t, assert.NoErr(t, err))
	assert.NotContains(t, rule, "old-rule")

	found, err := s.ListArchivedMemories(MemoryFilter{Keyword: "一句话"})
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, found, 1)
	assert.Contains(t, ArchivedMemoryListLine(found[0], time.Now()), "归档于 今天（superseded）")
	none, err := s.ListArchivedMemories(MemoryFilter{Keyword: "KEEP"})
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, none, 0)

	_, err = s.ArchiveMemory("missing", "", "me")
	assert.ErrMsgContains(t, err, "not found")

	// Restore brings it back with a fresh updated_at (beats the sync tombstone)
	// and the original created_at.
	time.Sleep(2 * time.Millisecond)
	restored, err := s.RestoreMemory("old-rule", "me2")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, before.CreatedAt, restored.CreatedAt)
	assert.True(t, restored.UpdatedAt > before.UpdatedAt)
	assert.Eq(t, "me2", restored.By)
	assert.Eq(t, "一句话", restored.Summary)
	archived, err = s.ReadArchivedMemories()
	assert.Require(t, assert.NoErr(t, err))
	assert.Len(t, archived, 0)
	_, err = s.RestoreMemory("old-rule", "me")
	assert.ErrMsgContains(t, err, "already exists")
	_, err = s.RestoreMemory("never", "me")
	assert.ErrMsgContains(t, err, "archived memory never not found")

	// The archive is a plain repository file; the synced memories.jsonl (the
	// sync snapshot source) no longer holds the key.
	_, err = s.ArchiveMemory("keep", "", "me")
	assert.Require(t, assert.NoErr(t, err))
	live, err := s.ReadMemories()
	assert.Require(t, assert.NoErr(t, err))
	for _, m := range live {
		assert.NotEq(t, "keep", m.Key)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, memoryArchiveFile))
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, string(data), `"archived_at"`)
}

func TestPromoteMemory(t *testing.T) {
	s := primeTestStore(t)
	h, err := s.SetMemoryPatch("ho", MemoryPatch{Content: "部署脚本放在 scripts/deploy.sh", Kind: strp(MemoryKindHandoff), Source: strp("job:j1"), By: "me"}, true)
	assert.Require(t, assert.NoErr(t, err))
	assert.NotEmpty(t, h.ExpiresAt)

	_, err = s.PromoteMemory("ho", MemoryKindHandoff, nil, "me")
	assert.ErrMsgContains(t, err, "rule or note")
	p, err := s.PromoteMemory("ho", MemoryKindNote, strp("部署脚本位置"), "me")
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, MemoryKindNote, p.Kind)
	assert.Empty(t, p.ExpiresAt)
	assert.Eq(t, "job:j1", p.Source)
	assert.Eq(t, "部署脚本位置", p.Summary)
	assert.Eq(t, h.CreatedAt, p.CreatedAt)

	_, err = s.SetMemoryPatch("ho2", MemoryPatch{Content: strings.Repeat("长", MemorySummaryRequiredRunes+1), Kind: strp(MemoryKindHandoff), By: "me"}, true)
	assert.Require(t, assert.NoErr(t, err))
	_, err = s.PromoteMemory("ho2", MemoryKindRule, nil, "me")
	assert.ErrMsgContains(t, err, "--summary")
	_, err = s.PromoteMemory("nope", MemoryKindRule, nil, "me")
	assert.ErrMsgContains(t, err, "not found")
}

func TestMemoryDefaultSourceAndDoctorIgnore(t *testing.T) {
	s := primeTestStore(t)
	ignore := []string{DoctorPathMissing}
	m, err := s.SetMemoryPatch("k", MemoryPatch{Content: "c", DefaultSource: "job:j9", DoctorIgnore: &ignore, By: "me"}, true)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "job:j9", m.Source)
	assert.Eq(t, ignore, m.DoctorIgnore)
	// An existing source is kept; an explicit one wins; doctor_ignore survives updates.
	m, err = s.SetMemoryPatch("k", MemoryPatch{Content: "c2", DefaultSource: "session:s1", By: "me"}, true)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "job:j9", m.Source)
	assert.Eq(t, ignore, m.DoctorIgnore)
	m, err = s.SetMemoryPatch("k", MemoryPatch{Content: "c3", Source: strp("plan:p1"), DefaultSource: "session:s1", DoctorIgnore: &[]string{}, By: "me"}, true)
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, "plan:p1", m.Source)
	assert.Empty(t, m.DoctorIgnore)
	assert.Contains(t, MemoryListLine(m, time.Now()), "· 来源 plan:p1")
	assert.Contains(t, MemoryDetail(Memory{Key: "x", Content: "y", MemoryMeta: MemoryMeta{DoctorIgnore: ignore}}, time.Now()), "doctor_ignore: path-missing")

	// doctor_ignore merges as a set across sync.
	base := SyncSnapshot{Memories: []Memory{{Key: "k", Content: "c", UpdatedAt: "1", MemoryMeta: MemoryMeta{DoctorIgnore: []string{"duplicate"}}}}}
	local := SyncSnapshot{Memories: []Memory{{Key: "k", Content: "c", UpdatedAt: "2", MemoryMeta: MemoryMeta{DoctorIgnore: []string{"duplicate", "path-missing"}}}}}
	remote := SyncSnapshot{Memories: []Memory{{Key: "k", Content: "c", UpdatedAt: "3", MemoryMeta: MemoryMeta{DoctorIgnore: []string{"commit-missing", "duplicate"}}}}}
	merged, _ := ThreeWayMerge(base, local, remote)
	assert.Eq(t, []string{"commit-missing", "duplicate", "path-missing"}, merged.Memories[0].DoctorIgnore)
}
