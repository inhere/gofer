package tracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func slugsOf(r DoctorReport, key string) []string {
	for _, e := range r.Memories {
		if e.Key == key {
			out := make([]string, 0, len(e.Findings))
			for _, f := range e.Findings {
				out = append(out, f.Slug)
			}
			return out
		}
	}
	return nil
}

func TestMemoryPathRefs(t *testing.T) {
	content := "见 `internal/tracker/prime.go:12` 与 docs/design/x.md。\n" +
		"server/worker 是两个角色，exec/检测类 job 不评论；`rule/note` 只查这两类。\n" +
		"链接 https://example.com/a/b、github.com/gookit/goutil、`$GOFER_CONFIG_DIR/x.sh`、`.gofer/tracker/*.jsonl`、`gofer memory show <key>`。\n" +
		"`GET /v1/meta`、/clear、~/bin/x、refs/heads/main、./scripts/run.sh、web/、[a](docs/a.md)"
	got := memoryPathRefs(content)
	want := []string{"internal/tracker/prime.go", "rule/note", "docs/design/x.md", "./scripts/run.sh", "web"}
	assert.Eq(t, want, got)
}

func TestMemoryCommitRefs(t *testing.T) {
	got := memoryCommitRefs("修复于 8d41dde5，见 (abc1234) 与 1234567 / deadbeef / plan-3fc4ecd5 / `0123456789abcdef0123456789abcdef01234567`")
	assert.Eq(t, []string{"8d41dde5", "abc1234", "0123456789abcdef0123456789abcdef01234567"}, got)
}

func TestDiagnoseMemories(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	assert.Require(t, assert.NoErr(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755)))
	assert.Require(t, assert.NoErr(t, os.WriteFile(filepath.Join(root, "docs", "ok.md"), []byte("x"), 0o644)))
	long := strings.Repeat("长", MemorySummaryRequiredRunes+1)
	items := []Memory{
		{Key: "h-old", Content: "交接 `docs/gone.md` 8d41dde5", UpdatedAt: "2026-09-01T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindHandoff}},
		{Key: "n-stale", Content: "old note", UpdatedAt: "2026-06-01T00:00:00Z"},
		{Key: "r-paths", Content: "see `docs/ok.md` and `docs/gone.md`, commit 8d41dde5", UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
		{Key: "r-long", Content: long, UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
		{Key: "s-long-ok", Content: long, UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{Kind: MemoryKindRule, Summary: "s"}},
		{Key: "web-build-a", Content: "web dist is live copy assets first then index html", UpdatedAt: "2026-10-08T00:00:00Z"},
		{Key: "web-build-b", Content: "web dist is live: copy assets first, then the index html", UpdatedAt: "2026-10-08T00:00:00Z"},
		{Key: "other-c", Content: "web dist is live copy assets first then index html", UpdatedAt: "2026-10-08T00:00:00Z"},
		{Key: "quiet", Content: "`docs/gone.md`", UpdatedAt: "2026-10-08T00:00:00Z", MemoryMeta: MemoryMeta{DoctorIgnore: []string{DoctorPathMissing}}},
	}
	var asked []string
	opts := DoctorOptions{Now: now, Roots: []string{root}, CommitsMissing: func(h []string) map[string]bool {
		asked = h
		return map[string]bool{"8d41dde5": true}
	}}
	r := DiagnoseMemories(items, opts)
	assert.Eq(t, []string{DoctorHandoffExpired}, slugsOf(r, "h-old")) // handoffs skip path / commit checks
	assert.Eq(t, []string{DoctorNoteStale}, slugsOf(r, "n-stale"))
	assert.Eq(t, []string{DoctorPathMissing, DoctorCommitMissing}, slugsOf(r, "r-paths"))
	assert.Eq(t, []string{DoctorSummaryMissing}, slugsOf(r, "r-long"))
	assert.Nil(t, slugsOf(r, "s-long-ok"))
	assert.Eq(t, []string{DoctorDuplicate}, slugsOf(r, "web-build-a"))
	assert.Eq(t, []string{DoctorDuplicate}, slugsOf(r, "web-build-b"))
	assert.Nil(t, slugsOf(r, "other-c")) // same content, different key prefix
	assert.Nil(t, slugsOf(r, "quiet"))
	assert.Eq(t, 1, r.Suppressed)
	assert.Eq(t, []string{"8d41dde5"}, asked) // handoff hashes are not even looked up
	for _, e := range r.Memories {
		if e.Key == "r-paths" {
			assert.Eq(t, "docs/gone.md", e.Findings[0].Detail)
		}
	}

	opts.Suppress = []string{DoctorDuplicate, DoctorNoteStale}
	r = DiagnoseMemories(items, opts)
	assert.Nil(t, slugsOf(r, "web-build-a"))
	assert.Nil(t, slugsOf(r, "n-stale"))
	assert.Eq(t, 4, r.Suppressed)
	assert.Contains(t, FormatDoctorReport(r), "r-paths [rule]")
}

func TestStoreDoctorGitAndPrimeMarker(t *testing.T) {
	root := t.TempDir()
	s, _, err := Init(root, "d", true)
	assert.Require(t, assert.NoErr(t, err))
	gitIn(t, root, "init")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-m", "base")
	head, err := exec.Command("git", "-C", root, "rev-parse", "--short=10", "HEAD").Output()
	assert.Require(t, assert.NoErr(t, err))
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339)
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{
			{Key: "good-rule", Content: "built at " + strings.TrimSpace(string(head)) + " see `.gofer/tracker/config.yaml`", UpdatedAt: stamp, MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
			{Key: "bad-rule", Content: "built at 0badc0ffee1 see `docs/missing.md`", UpdatedAt: stamp, MemoryMeta: MemoryMeta{Kind: MemoryKindRule}},
			{Key: "aged-note", Content: "an old note", UpdatedAt: now.Add(-100 * 24 * time.Hour).Format(time.RFC3339)},
		}, nil
	})))

	// Before any doctor run prime only knows the cheap checks.
	out, err := s.PrimeWith(PrimeOptions{Now: now})
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, out, "- good-rule: built")
	assert.Contains(t, out, "- bad-rule: built")
	assert.Contains(t, out, "aged-note · an old note · 100 天前（久未更新） "+PrimeStaleMarker)

	r, err := s.Doctor(now)
	assert.Require(t, assert.NoErr(t, err))
	assert.Nil(t, slugsOf(r, "good-rule"))
	assert.Eq(t, []string{DoctorPathMissing, DoctorCommitMissing}, slugsOf(r, "bad-rule"))
	assert.Eq(t, []string{DoctorNoteStale}, slugsOf(r, "aged-note"))

	// A fresh cache lets prime mark the git / path findings without running git.
	out, err = s.PrimeWith(PrimeOptions{Now: now})
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, out, "- bad-rule（"+PrimeStaleMarker+"）: built")
	assert.Contains(t, out, "- good-rule: built")

	// Config suppression applies to the cached findings too.
	assert.Require(t, assert.NoErr(t, s.UpdateConfig(func(c *Config) {
		c.Prime.Doctor = &PrimeDoctorConfig{Suppress: []string{DoctorPathMissing, DoctorCommitMissing}}
	})))
	out, err = s.PrimeWith(PrimeOptions{Now: now})
	assert.Require(t, assert.NoErr(t, err))
	assert.Contains(t, out, "- bad-rule: built")

	// Any memory write invalidates the cache (memories.jsonl size / mtime).
	assert.Require(t, assert.NoErr(t, s.UpdateConfig(func(c *Config) { c.Prime.Doctor = nil })))
	_, err = s.SetMemory("extra", "x", "me")
	assert.Require(t, assert.NoErr(t, err))
	assert.Nil(t, s.cachedDoctorFindings(now))
	assert.Nil(t, (&Store{Dir: s.Dir}).cachedDoctorFindings(now.Add(2*doctorCacheMaxAge)))
}
