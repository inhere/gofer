// Package bdmigrate moves a repository from bd (beads) to the gofer tracker:
// issues and memories, the managed instruction blocks, the agent hooks and
// core.hooksPath. It is a plan-then-apply pipeline: everything is read and
// computed first (a dry run stops there), the apply step writes backups and
// then the files, and the result is verified against the plan. .beads/ is
// never modified or removed, and bd is only ever invoked read-only.
//
// DEPRECATED(v0.115): remove in v0.130, once every workspace has migrated off bd (TRK-01).
package bdmigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

// Options configure one migration run.
type Options struct {
	Root           string
	Apply          bool
	Force          bool          // go ahead although bd looks active
	BDPath         string        // bd executable; "" = look up "bd" on PATH
	ActivityWindow time.Duration // 0 = DefaultActivityWindow
	ExportTimeout  time.Duration // 0 = 2 minutes
	Now            func() time.Time
}

// Report is everything the run found and did; Format renders it for humans.
type Report struct {
	Root       string         `json:"root"`
	Mode       string         `json:"mode"` // dry-run | apply
	Source     SourceReport   `json:"source"`
	Issues     int            `json:"issues"`
	Memories   int            `json:"memories"`
	Prefix     string         `json:"prefix"`
	Mapping    Mapping        `json:"mapping"`
	Activity   []string       `json:"activity,omitempty"`
	Blocks     []BlockPlan    `json:"blocks"`
	Hooks      []HookPlan     `json:"hooks"`
	Git        GitPlan        `json:"git"`
	Manual     []string       `json:"manual,omitempty"`
	Notes      []string       `json:"notes,omitempty"`
	Written    []string       `json:"written,omitempty"`
	BackupDir  string         `json:"backup_dir,omitempty"`
	Verified   *Verification  `json:"verified,omitempty"`
	SkippedMem int            `json:"memories_already_present,omitempty"`
	KeptIssues int            `json:"issues_kept_newer_local,omitempty"`
	PrimeLimit int            `json:"prime_memory_summary_limit,omitempty"`
	Skipped    map[string]int `json:"skipped_record_types,omitempty"`
}

// Verification compares the written tracker with the plan.
type Verification struct {
	Issues         int      `json:"issues"`
	IssuesMatching int      `json:"issues_matching_source"`
	Memories       int      `json:"memories"`
	MemoriesMatch  int      `json:"memories_matching_source"`
	BeadsUntouched bool     `json:"beads_issues_jsonl_unchanged"`
	Mismatched     []string `json:"mismatched_ids,omitempty"`
}

// More memories than primeMemoryThreshold get prime.memory_summary_limit = primeMemoryLimit.
const (
	primeMemoryThreshold = 20
	primeMemoryLimit     = 15
)

// ErrActive is returned when bd looks in use and --force was not given.
var ErrActive = errors.New("bd looks active in this repository")

// plan is the in-memory result of the read phase.
type plan struct {
	issues    []tracker.Issue
	memories  map[string]string
	prefix    string
	report    Report
	jsonlHash string
	have      map[string]bool // memory keys already in the tracker before this run
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Run executes the migration. Dry runs (Apply=false) never write anything.
func Run(opts Options) (Report, error) {
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return Report{}, err
	}
	opts.Root = root
	p, err := buildPlan(opts)
	if err != nil {
		return p.report, err
	}
	rep := &p.report
	if !opts.Apply {
		return *rep, nil
	}
	if len(rep.Activity) > 0 && !opts.Force {
		return *rep, fmt.Errorf("%w; close the bd sessions (or pass --force):\n  - %s", ErrActive, strings.Join(rep.Activity, "\n  - "))
	}
	if err := apply(opts, p); err != nil {
		return *rep, err
	}
	return *rep, nil
}

func buildPlan(opts Options) (*plan, error) {
	root := opts.Root
	p := &plan{report: Report{Root: root, Mode: "dry-run"}}
	rep := &p.report
	if opts.Apply {
		rep.Mode = "apply"
	}
	beads := filepath.Join(root, ".beads")
	if info, err := os.Stat(beads); err != nil || !info.IsDir() {
		return p, fmt.Errorf("%s has no .beads directory; nothing to migrate", root)
	}
	window := opts.ActivityWindow
	if window == 0 {
		window = DefaultActivityWindow
	}
	timeout := opts.ExportTimeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	// Activity first: it reads only file metadata and must run before we ask bd
	// anything, because bd itself touches its lock and journal files.
	activity := checkActivity(root, window, opts.now())
	jsonlPath := filepath.Join(beads, "issues.jsonl")
	var file dataset
	jsonlPresent := false
	if info, err := os.Stat(jsonlPath); err == nil && !info.IsDir() {
		jsonlPresent = true
		ds, err := readJSONL(jsonlPath)
		if err != nil {
			rep.Notes = append(rep.Notes, "issues.jsonl unreadable, ignored: "+err.Error())
			jsonlPresent = false
		} else {
			file = ds
			if b, err := os.ReadFile(jsonlPath); err == nil {
				sum := sha256.Sum256(b)
				p.jsonlHash = hex.EncodeToString(sum[:])
			}
		}
	}

	bin := opts.BDPath
	if bin == "" {
		if found, err := exec.LookPath("bd"); err == nil {
			bin = found
		}
	}
	var live dataset
	liveOK := false
	runner := &bdRunner{bin: bin, root: root, timeout: timeout}
	// bd drops an empty `.beads.gate.lock` next to .beads when it runs. If the
	// file did not exist before we asked, our own export created it: remove it again.
	gateLock := filepath.Join(root, ".beads.gate.lock")
	_, gateErr := os.Stat(gateLock)
	hadGateLock := gateErr == nil
	defer func() {
		if !hadGateLock {
			if info, err := os.Stat(gateLock); err == nil && info.Size() == 0 && !info.IsDir() {
				_ = os.Remove(gateLock)
			}
		}
	}()
	if bin == "" {
		rep.Notes = append(rep.Notes, "bd executable not found on PATH; using .beads/issues.jsonl only (it may be stale and carries no memories unless exported with them)")
	} else {
		var lerr error
		live, lerr = runner.exportLive()
		if lerr != nil {
			rep.Notes = append(rep.Notes, "live bd export unavailable, falling back to issues.jsonl: "+lerr.Error())
		} else {
			liveOK = true
		}
	}
	rep.Notes = append(rep.Notes, runner.notes...)

	var primary dataset
	switch {
	case liveOK:
		primary = live
		rep.Source = SourceReport{Used: "bd export", LiveIssues: len(live.issues), LiveMemories: len(live.memories), JSONLPresent: jsonlPresent}
		if jsonlPresent {
			rep.Source.JSONLIssues = len(file.issues)
			compareSources(live, file, &rep.Source)
			if rep.Source.Stale {
				rep.Notes = append(rep.Notes, fmt.Sprintf("issues.jsonl is behind the bd database: %d issue(s) only in the database, %d only in the file, %d with a different updated_at; the database is used", len(rep.Source.OnlyLive), len(rep.Source.OnlyJSONL), len(rep.Source.NewerLive)))
			}
		} else {
			rep.Notes = append(rep.Notes, "no .beads/issues.jsonl; the data comes from the bd database only")
		}
	case jsonlPresent:
		primary = file
		rep.Source = SourceReport{Used: "issues.jsonl", JSONLIssues: len(file.issues), JSONLPresent: true}
		if bin != "" {
			if mem, err := runner.memoriesLive(); err == nil {
				for k, v := range mem {
					if _, ok := primary.memories[k]; !ok {
						primary.memories[k] = v
					}
				}
			} else {
				rep.Notes = append(rep.Notes, "memories not imported: "+err.Error())
			}
		} else if len(primary.memories) == 0 {
			rep.Notes = append(rep.Notes, "memories not imported: bd is unavailable and issues.jsonl holds none")
		}
	default:
		return p, errors.New("no data source: bd export failed and .beads/issues.jsonl is missing")
	}
	rep.Skipped = primary.skipped
	if len(rep.Skipped) == 0 {
		rep.Skipped = nil
	}
	rep.Activity = append(activity, leaseFindings(primary.issues, opts.now())...)

	p.issues, rep.Mapping = convertIssues(primary.issues)
	p.memories = primary.memories
	p.prefix = inferPrefix(primary.issues)
	rep.Issues, rep.Memories, rep.Prefix = len(p.issues), len(p.memories), p.prefix
	if p.prefix == "" {
		rep.Notes = append(rep.Notes, "issue prefix could not be inferred; the directory name will be used")
	}

	blocks, err := planBlocks(root)
	if err != nil {
		return p, err
	}
	rep.Blocks = blocks
	for _, agent := range []string{"claude", "codex"} {
		hp, err := planHooks(agent, hookFile(root, agent))
		if err != nil {
			return p, err
		}
		rep.Hooks = append(rep.Hooks, hp)
	}
	rep.Git = planGit(root)
	if _, err := os.Stat(filepath.Join(beads, "PRIME.md")); err == nil {
		rep.Notes = append(rep.Notes, ".beads/PRIME.md is a bd-only prime override and is not migrated; `gofer repo prime` follows the `prime:` section of .gofer/tracker/config.yaml instead")
	}
	after := map[string]string{}
	for _, b := range blocks {
		if b.New != "" {
			after[b.File] = b.New
		}
	}
	for _, h := range rep.Hooks {
		if h.New != nil {
			relPath, _ := filepath.Rel(root, h.Path)
			after[filepath.ToSlash(relPath)] = string(h.New)
		}
	}
	rep.Manual = scanManual(root, after)
	for _, h := range rep.Hooks {
		rep.Manual = append(rep.Manual, h.Manual...)
	}
	if existing, err := tracker.Discover(root, filepath.Join(root, ".gofer", "tracker")); err == nil {
		if cfg, err := existing.ReadConfig(); err == nil && p.prefix != "" && cfg.Prefix != p.prefix {
			rep.Notes = append(rep.Notes, fmt.Sprintf("a tracker already exists with prefix %q (bd ids use %q); the existing prefix is kept", cfg.Prefix, p.prefix))
		}
	}
	return p, nil
}

func apply(opts Options, p *plan) error {
	root := opts.Root
	rep := &p.report
	store, _, err := tracker.Init(root, p.prefix, true)
	if err != nil {
		return err
	}
	// A bd repository can carry dozens of memories; listing them all in every
	// SessionStart prime would drown the issue sections (bd users solved this with
	// a PRIME.md override: recall on demand). Cap the summaries for big sets.
	if len(p.memories) > primeMemoryThreshold {
		if cfg, err := store.ReadConfig(); err == nil && cfg.Prime.MemorySummaryLimit == nil {
			limit := primeMemoryLimit
			if err := store.UpdateConfig(func(c *tracker.Config) { c.Prime.MemorySummaryLimit = &limit }); err != nil {
				return err
			}
			rep.PrimeLimit = limit
		}
	}
	// Backups first: everything the migration is about to rewrite.
	stamp := opts.now().UTC().Format("20060102T150405Z")
	rep.BackupDir = filepath.Join(store.Dir, ".local", "migrate-backup", stamp)
	backup := func(path string) error {
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(root, path)
		dst := filepath.Join(rep.BackupDir, relPath)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	}
	for _, b := range rep.Blocks {
		if b.New != "" {
			if err := backup(filepath.Join(root, b.File)); err != nil {
				return err
			}
		}
	}
	for _, h := range rep.Hooks {
		if h.New != nil {
			if err := backup(h.Path); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"issues.jsonl", "memories.jsonl", "config.yaml"} {
		if err := backup(filepath.Join(store.Dir, name)); err != nil {
			return err
		}
	}

	// Issues: a newer local copy (edited in gofer since an earlier run) wins.
	kept := 0
	if err := store.UpdateIssues(func(existing []tracker.Issue) ([]tracker.Issue, error) {
		index := make(map[string]int, len(existing))
		for i := range existing {
			index[existing[i].ID] = i
		}
		for _, incoming := range p.issues {
			if i, ok := index[incoming.ID]; ok {
				if incoming.UpdatedAt > existing[i].UpdatedAt {
					existing[i] = incoming
				} else {
					kept++
				}
				continue
			}
			index[incoming.ID] = len(existing)
			existing = append(existing, incoming)
		}
		return existing, nil
	}); err != nil {
		return err
	}
	rep.KeptIssues = kept
	rep.Written = append(rep.Written, ".gofer/tracker/issues.jsonl")
	if len(p.memories) > 0 {
		now := tracker.Now()
		skipped := 0
		if err := store.UpdateMemories(func(existing []tracker.Memory) ([]tracker.Memory, error) {
			have := make(map[string]bool, len(existing))
			for _, m := range existing {
				have[m.Key] = true
			}
			p.have = have
			keys := make([]string, 0, len(p.memories))
			for k := range p.memories {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if have[k] {
					skipped++
					continue
				}
				existing = append(existing, tracker.Memory{Key: k, Content: p.memories[k], UpdatedAt: now, By: "bd-migrate"})
			}
			return existing, nil
		}); err != nil {
			return err
		}
		rep.SkippedMem = skipped
		rep.Written = append(rep.Written, ".gofer/tracker/memories.jsonl")
	}

	for _, b := range rep.Blocks {
		if b.New == "" {
			continue
		}
		if err := tracker.AtomicWriteFile(filepath.Join(root, b.File), []byte(b.New)); err != nil {
			return err
		}
		rep.Written = append(rep.Written, b.File)
	}
	for _, h := range rep.Hooks {
		if h.New == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(h.Path), 0o755); err != nil {
			return err
		}
		if err := tracker.AtomicWriteFile(h.Path, h.New); err != nil {
			return err
		}
		relPath, _ := filepath.Rel(root, h.Path)
		rep.Written = append(rep.Written, filepath.ToSlash(relPath))
	}
	if rep.Git.Action == "unset" {
		if err := unsetHooksPath(root); err != nil {
			return err
		}
		rep.Written = append(rep.Written, "git config core.hooksPath (unset)")
	}
	v, err := verify(store, p, filepath.Join(root, ".beads", "issues.jsonl"))
	if err != nil {
		return err
	}
	rep.Verified = &v
	if v.IssuesMatching != len(p.issues) || v.MemoriesMatch != len(p.memories) || !v.BeadsUntouched {
		return fmt.Errorf("verification failed: %d/%d issues and %d/%d memories match, .beads unchanged=%v (backups in %s)", v.IssuesMatching, len(p.issues), v.MemoriesMatch, len(p.memories), v.BeadsUntouched, rep.BackupDir)
	}
	return nil
}

// verify re-reads the tracker and compares it with what was planned. Issues
// whose local copy is newer than the source (a repeated run) count as matching.
func verify(store *tracker.Store, p *plan, jsonlPath string) (Verification, error) {
	var v Verification
	issues, err := store.ReadIssues()
	if err != nil {
		return v, err
	}
	memories, err := store.ReadMemories()
	if err != nil {
		return v, err
	}
	v.Issues, v.Memories = len(issues), len(memories)
	byID := make(map[string]tracker.Issue, len(issues))
	for _, it := range issues {
		byID[it.ID] = it
	}
	for _, want := range p.issues {
		got, ok := byID[want.ID]
		if ok && (sameIssue(got, want) || got.UpdatedAt > want.UpdatedAt) {
			v.IssuesMatching++
		} else if len(v.Mismatched) < 20 {
			v.Mismatched = append(v.Mismatched, want.ID)
		}
	}
	mem := make(map[string]string, len(memories))
	for _, m := range memories {
		mem[m.Key] = m.Content
	}
	for k, want := range p.memories {
		if got, ok := mem[k]; ok && (got == want || p.have[k]) {
			v.MemoriesMatch++
		}
	}
	v.BeadsUntouched = true
	if p.jsonlHash != "" {
		if b, err := os.ReadFile(jsonlPath); err == nil {
			sum := sha256.Sum256(b)
			v.BeadsUntouched = hex.EncodeToString(sum[:]) == p.jsonlHash
		}
	}
	return v, nil
}

// sameIssue compares through JSON so nil and empty slices are equal.
func sameIssue(a, b tracker.Issue) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return reflect.DeepEqual(aj, bj)
}
