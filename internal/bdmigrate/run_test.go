//go:build !windows

package bdmigrate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

const fixtureExport = `{"_type":"issue","id":"demo-aaa","title":"epic","status":"open","priority":1,"issue_type":"epic","labels":["proj01"],"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
{"_type":"issue","id":"demo-aaa.1","title":"child","description":"d","status":"closed","priority":2,"issue_type":"task","labels":["proj02","x"],"notes":"n","closed_at":"2026-01-05T00:00:00Z","close_reason":"done","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-05T00:00:00Z","dependencies":[{"issue_id":"demo-aaa.1","depends_on_id":"demo-aaa","type":"parent-child","metadata":"{}"}],"comments":[{"id":"1","author":"rev","text":"ok","created_at":"2026-01-03T00:00:00Z"}]}
{"_type":"issue","id":"demo-bbb","title":"third","status":"open","priority":3,"issue_type":"bug","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","dependencies":[{"issue_id":"demo-bbb","depends_on_id":"demo-aaa","type":"blocks","metadata":"{}"}]}
{"_type":"memory","key":"how-to","value":"use the thing"}
`

// fakeBD writes a bd stand-in that logs its arguments, serves fixtureExport and
// the memories map, and can be told to fail or demand the schema-skew override.
func fakeBD(t *testing.T, mode string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "bd.log")
	exportFile := filepath.Join(dir, "export.jsonl")
	if err := os.WriteFile(exportFile, []byte(fixtureExport), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$BD_IGNORE_SCHEMA_SKEW|$*\" >> '" + log + "'\n"
	script += "touch .beads.gate.lock\n"
	switch mode {
	case "fail":
		script += "echo 'Error: no beads database found' >&2\nexit 1\n"
	case "skew":
		script += "if [ \"$BD_IGNORE_SCHEMA_SKEW\" != 1 ]; then echo 'Error: failed to open database: schema version mismatch' >&2; exit 1; fi\n"
		fallthrough
	default:
		script += "case \"$*\" in\n  *export*) cat '" + exportFile + "' ;;\n  *memories*) echo '{\"how-to\":\"use the thing\",\"schema_version\":1}' ;;\n  *) exit 2 ;;\nesac\n"
	}
	bin = filepath.Join(dir, "bd")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func newBdRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "issues.jsonl"), `{"_type":"issue","id":"demo-aaa","title":"epic (stale)","status":"open","priority":1,"issue_type":"epic","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`+"\n")
	writeFile(t, filepath.Join(root, ".beads", "hooks", "pre-commit"), "#!/usr/bin/env sh\n# --- BEGIN BEADS INTEGRATION v1.3.0 ---\nbd hooks run pre-commit \"$@\"\n# --- END BEADS INTEGRATION v1.3.0 ---\n")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Project\n\nhand written rules\n\n"+bdInteg+"\n## After\nkeep me\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@workspace.md\n"+bdInteg+bdCodex)
	writeFile(t, filepath.Join(root, "workspace.md"), "# ws\n- use `bd create ... -l proj01` to split\n")
	writeFile(t, filepath.Join(root, ".claude", "settings.json"), "{\n  \"hooks\": {\n    \"SessionStart\": [\n      {\n        \"hooks\": [\n          {\n            \"command\": \"bd prime\",\n            \"type\": \"command\"\n          }\n        ],\n        \"matcher\": \"\"\n      }\n    ]\n  }\n}\n")
	for _, args := range [][]string{{"init", "-q", "."}, {"config", "core.hooksPath", ".beads/hooks"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func later() func() time.Time {
	at := time.Now().Add(time.Hour)
	return func() time.Time { return at }
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunDryRunReportsStaleJSONLAndWritesNothing(t *testing.T) {
	root := newBdRepo(t)
	bin, log := fakeBD(t, "ok")
	rep, err := Run(Options{Root: root, BDPath: bin, Now: later()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source.Used != "bd export" || rep.Source.LiveIssues != 3 || rep.Source.JSONLIssues != 1 || !rep.Source.Stale || len(rep.Source.OnlyLive) != 2 || len(rep.Source.NewerLive) != 1 {
		t.Fatalf("source: %+v", rep.Source)
	}
	if rep.Issues != 3 || rep.Memories != 1 || rep.Prefix != "demo" || rep.Mode != "dry-run" {
		t.Fatalf("report: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(root, ".gofer")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .gofer: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "CLAUDE.md")); !strings.Contains(got, "BEGIN BEADS INTEGRATION") {
		t.Fatal("dry-run edited CLAUDE.md")
	}
	if _, err := os.Stat(filepath.Join(root, ".beads.gate.lock")); !os.IsNotExist(err) {
		t.Fatal("the gate lock created by our own bd export must be removed again")
	}
	// bd is only ever called read-only.
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, log)), "\n") {
		if !strings.Contains(line, "|--readonly ") {
			t.Fatalf("bd call without --readonly: %q", line)
		}
	}
	text := rep.Format()
	for _, want := range []string{"STALE", "dry-run", "3 issues", "workspace.md:2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text missing %q:\n%s", want, text)
		}
	}
	if rep.Git.Action != "unset" || len(rep.Manual) == 0 {
		t.Fatalf("git/manual: %+v / %v", rep.Git, rep.Manual)
	}
}

func TestRunApplyMigratesEverythingAndVerifies(t *testing.T) {
	root := newBdRepo(t)
	bin, _ := fakeBD(t, "ok")
	beforeJSONL := readFile(t, filepath.Join(root, ".beads", "issues.jsonl"))
	rep, err := Run(Options{Root: root, Apply: true, BDPath: bin, Now: later()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified == nil || rep.Verified.IssuesMatching != 3 || rep.Verified.MemoriesMatch != 1 || !rep.Verified.BeadsUntouched {
		t.Fatalf("verification: %+v", rep.Verified)
	}
	store, err := tracker.Discover(root, "")
	if err != nil {
		t.Fatal(err)
	}
	issues, _ := store.ReadIssues()
	if len(issues) != 3 {
		t.Fatalf("issues: %+v", issues)
	}
	child, err := store.Issue("demo-aaa.1")
	if err != nil || child.Parent != "demo-aaa" || strings.Join(child.Tags, ",") != "proj02,x" || child.CloseReason != "done" || len(child.Comments) != 1 {
		t.Fatalf("child: %+v %v", child, err)
	}
	if cfg, _ := store.ReadConfig(); cfg.Prefix != "demo" {
		t.Fatalf("tracker prefix must follow the bd ids, got %q", cfg.Prefix)
	}
	if mem, err := store.Memory("how-to"); err != nil || mem.Content != "use the thing" {
		t.Fatalf("memory: %+v %v", mem, err)
	}
	// Label filter works on the migrated data (sub-project split).
	if got, _ := store.ListIssues(tracker.IssueFilter{Tags: []string{"proj01"}, All: true}); len(got) != 1 || got[0].ID != "demo-aaa" {
		t.Fatalf("label filter: %+v", got)
	}

	claude := readFile(t, filepath.Join(root, "CLAUDE.md"))
	if claude != "# Project\n\nhand written rules\n\n"+tracker.ManagedBlock()+"\n## After\nkeep me\n" {
		t.Fatalf("CLAUDE.md:\n%s", claude)
	}
	agents := readFile(t, filepath.Join(root, "AGENTS.md"))
	if agents != "@workspace.md\n"+tracker.ManagedBlock() {
		t.Fatalf("AGENTS.md:\n%s", agents)
	}
	settings := readFile(t, filepath.Join(root, ".claude", "settings.json"))
	if !strings.Contains(settings, "gofer repo prime --hook-json --agent claude") || strings.Contains(settings, "bd prime") {
		t.Fatalf("settings:\n%s", settings)
	}
	if out, _ := exec.Command("git", "-C", root, "config", "--local", "--get", "core.hooksPath").Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("core.hooksPath still set: %q", out)
	}
	if got := readFile(t, filepath.Join(root, ".beads", "issues.jsonl")); got != beforeJSONL {
		t.Fatal(".beads/issues.jsonl changed")
	}
	if _, err := os.Stat(filepath.Join(root, ".beads", "hooks", "pre-commit")); err != nil {
		t.Fatal(".beads must be kept")
	}
	// Backups hold the original instruction files.
	if b := readFile(t, filepath.Join(rep.BackupDir, "CLAUDE.md")); !strings.Contains(b, "BEGIN BEADS INTEGRATION") {
		t.Fatalf("backup of CLAUDE.md: %q", b)
	}
	// A second run is a no-op for the data and the files.
	first := readFile(t, filepath.Join(root, ".gofer", "tracker", "issues.jsonl"))
	rep2, err := Run(Options{Root: root, Apply: true, BDPath: bin, Now: later()})
	if err != nil {
		t.Fatal(err)
	}
	if again := readFile(t, filepath.Join(root, ".gofer", "tracker", "issues.jsonl")); again != first {
		t.Fatal("repeat migration changed the issues")
	}
	if readFile(t, filepath.Join(root, "CLAUDE.md")) != claude || len(rep2.Written) > 2 {
		t.Fatalf("repeat run rewrote files: %v", rep2.Written)
	}
}

func TestRunRefusesWhileBdLooksActiveUnlessForced(t *testing.T) {
	root := newBdRepo(t)
	bin, _ := fakeBD(t, "ok")
	// "now" is the real now: the .beads files were written seconds ago.
	rep, err := Run(Options{Root: root, Apply: true, BDPath: bin})
	if !errors.Is(err, ErrActive) || len(rep.Activity) == 0 {
		t.Fatalf("want ErrActive, got %v (activity %v)", err, rep.Activity)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".gofer")); !os.IsNotExist(statErr) {
		t.Fatal("a refused run must not write anything")
	}
	if _, err := Run(Options{Root: root, Apply: true, Force: true, BDPath: bin}); err != nil {
		t.Fatalf("--force must proceed: %v", err)
	}
}

func TestRunFallsBackToJSONLWhenBdFails(t *testing.T) {
	root := newBdRepo(t)
	bin, _ := fakeBD(t, "fail")
	rep, err := Run(Options{Root: root, BDPath: bin, Now: later()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source.Used != "issues.jsonl" || rep.Issues != 1 || rep.Memories != 0 {
		t.Fatalf("fallback: %+v", rep.Source)
	}
	joined := strings.Join(rep.Notes, "\n")
	if !strings.Contains(joined, "falling back") || !strings.Contains(joined, "memories not imported") {
		t.Fatalf("notes: %v", rep.Notes)
	}
	// No bd at all, and no jsonl: nothing to read.
	bare := t.TempDir()
	writeFile(t, filepath.Join(bare, ".beads", "config.yaml"), "x: 1\n")
	if _, err := Run(Options{Root: bare, BDPath: filepath.Join(bare, "no-such-bd"), Now: later()}); err == nil {
		t.Fatal("no data source must be an error")
	}
	if _, err := Run(Options{Root: t.TempDir()}); err == nil {
		t.Fatal("a directory without .beads must be an error")
	}
}

func TestRunRetriesWithSchemaSkewOverride(t *testing.T) {
	root := newBdRepo(t)
	bin, log := fakeBD(t, "skew")
	rep, err := Run(Options{Root: root, BDPath: bin, Now: later()})
	if err != nil || rep.Source.Used != "bd export" {
		t.Fatalf("skew retry: %v %+v", err, rep.Source)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "BD_IGNORE_SCHEMA_SKEW") || !strings.Contains(readFile(t, log), "1|--readonly export") {
		t.Fatalf("notes %v\nlog %s", rep.Notes, readFile(t, log))
	}
}

func TestRunKeepsPreexistingGateLock(t *testing.T) {
	root := newBdRepo(t)
	writeFile(t, filepath.Join(root, ".beads.gate.lock"), "")
	bin, _ := fakeBD(t, "ok")
	if _, err := Run(Options{Root: root, BDPath: bin, Now: later()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".beads.gate.lock")); err != nil {
		t.Fatal("a lock file that existed before must stay")
	}
}

func TestGitPlanKeepsHooksPathWhenCustomHooksOrNotTopLevel(t *testing.T) {
	root := newBdRepo(t)
	if got := planGit(root); got.Action != "unset" || !got.PointsAtBeads {
		t.Fatalf("bd-only hooks: %+v", got)
	}
	writeFile(t, filepath.Join(root, ".beads", "hooks", "pre-push"), "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v1 ---\nbd hooks run\n# --- END BEADS INTEGRATION v1 ---\nmake lint\n")
	got := planGit(root)
	if got.Action != "keep" || len(got.Custom) != 1 || got.Custom[0] != "pre-push" {
		t.Fatalf("custom hook must keep core.hooksPath: %+v", got)
	}
	// A directory inside another repository must never touch that repository's config.
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(sub, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := planGit(sub); got.Action != "none" || !strings.Contains(got.Reason, "inside the git repository") {
		t.Fatalf("nested directory: %+v", got)
	}
}

func TestCheckActivityFindsLocksRecentWritesAndLeases(t *testing.T) {
	root := t.TempDir()
	beads := filepath.Join(root, ".beads")
	old := time.Now().Add(-2 * time.Hour)
	touch := func(rel string, at time.Time) string {
		p := filepath.Join(beads, rel)
		writeFile(t, p, "x")
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	touch("issues.jsonl", old)
	touch("embeddeddolt/db/.dolt/noms/manifest", time.Now()) // dolt internals are touched by any read: ignored
	gate := touch("embeddeddolt.gate.lock", old)
	if got := checkActivity(root, 5*time.Minute, time.Now()); len(got) != 0 {
		t.Fatalf("quiet repository flagged: %v", got)
	}
	touch("last-touched", time.Now())
	if got := checkActivity(root, 5*time.Minute, time.Now()); len(got) != 1 || !strings.Contains(got[0], "last-touched") {
		t.Fatalf("recent write: %v", got)
	}
	f, err := os.Open(gate)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if got := checkActivity(root, time.Nanosecond, time.Now()); len(got) != 1 || !strings.Contains(got[0], "lock held") {
		t.Fatalf("held lock: %v", got)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	soon := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	past := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	got := leaseFindings([]bdRecord{{ID: "a", Status: "in_progress", LeaseExpiresAt: soon}, {ID: "b", Status: "in_progress", LeaseExpiresAt: past}, {ID: "c", Status: "closed", LeaseExpiresAt: soon}}, time.Now())
	if len(got) != 1 || !strings.Contains(got[0], "lease on a") {
		t.Fatalf("leases: %v", got)
	}
}

// A read-only bd export (our own dry-run) refreshes the gate locks' mtime; that
// must not make the following --apply look like a live writer. A gate lock that
// is actually HELD still does.
func TestCheckActivityIgnoresGateLockMtimeButNotHeldLock(t *testing.T) {
	root := t.TempDir()
	beads := filepath.Join(root, ".beads")
	old := time.Now().Add(-2 * time.Hour)
	writeFile(t, filepath.Join(beads, "issues.jsonl"), "x")
	_ = os.Chtimes(filepath.Join(beads, "issues.jsonl"), old, old)
	innerGate := filepath.Join(beads, "embeddeddolt.gate.lock")
	rootGate := filepath.Join(root, ".beads.gate.lock")
	writeFile(t, innerGate, "")
	writeFile(t, rootGate, "")
	now := time.Now() // the dry-run just touched both
	for _, p := range []string{innerGate, rootGate} {
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := checkActivity(root, 5*time.Minute, time.Now()); len(got) != 0 {
		t.Fatalf("fresh gate-lock mtime flagged: %v", got)
	}
	for _, p := range []string{innerGate, rootGate} {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		got := checkActivity(root, 5*time.Minute, time.Now())
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		if len(got) != 1 || !strings.Contains(got[0], "lock held") || !strings.Contains(got[0], filepath.Base(p)) {
			t.Fatalf("held %s: %v", p, got)
		}
	}
	// other recent writes still count
	writeFile(t, filepath.Join(beads, "interactions.jsonl"), "x")
	if got := checkActivity(root, 5*time.Minute, time.Now()); len(got) != 1 || !strings.Contains(got[0], "interactions.jsonl") {
		t.Fatalf("recent interactions write: %v", got)
	}
}

func TestRunCapsPrimeMemorySummariesForLargeMemorySets(t *testing.T) {
	root := newBdRepo(t)
	var export strings.Builder
	export.WriteString(`{"_type":"issue","id":"demo-aaa","title":"one","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}` + "\n")
	for i := 0; i < 25; i++ {
		export.WriteString(`{"_type":"memory","key":"k` + string(rune('a'+i)) + `","value":"v"}` + "\n")
	}
	bin, _ := fakeBD(t, "ok")
	// point the fake at the big export
	script := readFile(t, bin)
	exportPath := filepath.Join(filepath.Dir(bin), "export.jsonl")
	writeFile(t, exportPath, export.String())
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(Options{Root: root, Apply: true, BDPath: bin, Now: later()})
	if err != nil || rep.PrimeLimit != primeMemoryLimit || rep.Memories != 25 {
		t.Fatalf("run: %v %+v", err, rep)
	}
	store, _ := tracker.Discover(root, "")
	if cfg, _ := store.ReadConfig(); cfg.Prime.MemorySummaryLimit == nil || *cfg.Prime.MemorySummaryLimit != primeMemoryLimit {
		t.Fatalf("config: %+v", cfg.Prime)
	}
	body, _ := store.Prime()
	if !strings.Contains(body, "另有 10 条：`gofer memory ls <关键字>`") {
		t.Fatalf("prime:\n%s", body)
	}
}
