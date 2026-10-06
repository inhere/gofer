package bdmigrate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultActivityWindow is how recent a write to .beads counts as "in use".
const DefaultActivityWindow = 5 * time.Minute

// checkActivity must run BEFORE bd is asked anything: even a read-only bd open
// rewrites dolt's journal/manifest files, so the dolt directory itself is not a
// usable "recent write" signal (it would flag our own export). Writes are
// recognised by bd's top-level bookkeeping files (last-touched, issues.jsonl,
// export-state.json, interactions.jsonl) and the backup directory instead.
//
// checkActivity looks for signs that bd (or something using it) is live in
// root, so a migration does not fork the data: held lock files, running bd /
// dolt processes, recent writes under .beads, unexpired bd claim leases.
// Every probe is read-only. Findings are human-readable lines.
func checkActivity(root string, window time.Duration, now time.Time) []string {
	beads := filepath.Join(root, ".beads")
	var findings []string
	// 1. lock files currently held by a process.
	for _, path := range lockCandidates(beads) {
		if lockHeld(path) {
			findings = append(findings, "lock held by a running process: "+rel(root, path))
		}
	}
	// 2. running bd / dolt processes working in this repository.
	findings = append(findings, findBdProcesses(root)...)
	// 3. recent writes.
	cutoff := now.Add(-window)
	for _, path := range recentFiles(beads, cutoff) {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		findings = append(findings, fmt.Sprintf("written %s ago: %s", now.Sub(info.ModTime()).Round(time.Second), rel(root, path)))
		if len(findings) > 12 {
			break
		}
	}
	return findings
}

// leaseFindings reports bd claim leases that have not expired yet: a bd worker
// is mid-task on that issue.
func leaseFindings(records []bdRecord, now time.Time) []string {
	var findings []string
	for _, rec := range records {
		if rec.LeaseExpiresAt == "" || rec.Status == "closed" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, rec.LeaseExpiresAt); err == nil && t.After(now) {
			findings = append(findings, fmt.Sprintf("bd claim lease on %s runs until %s", rec.ID, rec.LeaseExpiresAt))
		}
	}
	return findings
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

// lockCandidates lists the lock files bd's embedded dolt takes.
func lockCandidates(beads string) []string {
	var out []string
	add := func(p string) {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			out = append(out, p)
		}
	}
	add(filepath.Join(beads, "embeddeddolt.gate.lock"))
	add(filepath.Join(filepath.Dir(beads), ".beads.gate.lock"))
	_ = filepath.WalkDir(beads, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "hooks" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if (name == "LOCK" || name == ".lock" || strings.HasSuffix(name, ".lock")) && p != filepath.Join(beads, "embeddeddolt.gate.lock") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// isGateLock reports bd's advisory gate locks (`embeddeddolt.gate.lock`,
// `.beads.gate.lock`): any bd call, even a read-only export, touches them.
func isGateLock(name string) bool { return strings.HasSuffix(name, ".gate.lock") }

// recentFiles returns files under .beads modified after cutoff. hooks/ is
// skipped (not a sign of use) and so is embeddeddolt/ (touched by any open,
// including a read-only export; see checkActivity), and so are the *.gate.lock
// files (see isGateLock).
func recentFiles(beads string, cutoff time.Time) []string {
	var out []string
	budget := 20000
	_ = filepath.WalkDir(beads, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if budget--; budget < 0 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if d.Name() == "hooks" || d.Name() == "embeddeddolt" {
				return filepath.SkipDir
			}
			return nil
		}
		// bd refreshes its gate lock's mtime on every open, read-only exports (our
		// own dry-run) included, so the mtime says nothing about writes; whether the
		// lock is HELD is the signal, and checkActivity probes that separately.
		if isGateLock(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(cutoff) {
			out = append(out, p)
		}
		return nil
	})
	return out
}
