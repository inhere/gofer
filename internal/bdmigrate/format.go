package bdmigrate

import (
	"fmt"
	"sort"
	"strings"
)

// Format renders the report as the text `gofer repo migrate` prints.
func (r Report) Format() string {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	p("== bd -> gofer tracker migration (%s) ==", r.Mode)
	p("repository: %s", r.Root)
	s := r.Source
	p("\n[source] %s", s.Used)
	if s.Used == "bd export" {
		p("  bd database: %d issues, %d memories", s.LiveIssues, s.LiveMemories)
	}
	if s.JSONLPresent {
		p("  .beads/issues.jsonl: %d issues", s.JSONLIssues)
		if s.Used == "bd export" {
			if s.Stale {
				p("  jsonl is STALE vs the database: +%d only in database, %d only in file, %d changed%s", len(s.OnlyLive), len(s.OnlyJSONL), len(s.NewerLive), idList(s.OnlyLive, " (new: %s)"))
			} else {
				p("  jsonl matches the database")
			}
		}
	} else {
		p("  .beads/issues.jsonl: absent")
	}
	if len(r.Skipped) > 0 {
		p("  ignored record types: %s", countList(r.Skipped))
	}
	p("\n[import] %d issues, %d memories, id prefix %q", r.Issues, r.Memories, r.Prefix)
	m := r.Mapping
	p("  parent from parent-child dependency: %d, from dotted id: %d, extra parent-child edges kept as deps: %d", m.ParentFromDep, m.ParentFromDotID, m.ExtraParentDeps)
	p("  comments: %d, notes converted: %d, dependency kinds: %s", m.CommentsImported, m.NotesConverted, countList(m.DepTypes))
	if len(m.StatusRemapped) > 0 {
		p("  unknown statuses mapped to open + tag bd:<status>: %s", countList(m.StatusRemapped))
	}
	if m.LeasesDropped > 0 || m.DepMetaDropped > 0 || m.DanglingDeps > 0 {
		p("  not carried over: bd claim lease/heartbeat on %d issue(s), created_at/by on %d dependency edge(s); dangling dependency targets kept: %d", m.LeasesDropped, m.DepMetaDropped, m.DanglingDeps)
	}
	p("\n[activity guard]")
	if len(r.Activity) == 0 {
		p("  no sign that bd is in use")
	}
	for _, a := range r.Activity {
		p("  ! %s", a)
	}
	p("\n[instruction files]")
	for _, bl := range r.Blocks {
		p("  %-10s %s (bd blocks removed: %d, gofer block: %v)", bl.File, bl.Action, bl.Removed, bl.Gofer)
	}
	p("\n[agent hooks]")
	if len(r.Hooks) == 0 {
		p("  none")
	}
	for _, h := range r.Hooks {
		switch {
		case !h.Exists:
			p("  %s: absent, not created (`gofer repo init` installs the SessionStart prime hook)", relTo(r.Root, h.Path))
		case len(h.Changes) == 0:
			p("  %s: no change", relTo(r.Root, h.Path))
		}
		for _, c := range h.Changes {
			p("  %s: %s", relTo(r.Root, h.Path), c)
		}
	}
	p("\n[git]")
	g := r.Git
	switch {
	case g.HooksPath == "" && g.Reason != "":
		p("  %s", g.Reason)
	case g.HooksPath == "":
		p("  core.hooksPath not set")
	default:
		p("  core.hooksPath = %s -> %s", g.HooksPath, g.Action)
		if g.Reason != "" {
			p("  %s", g.Reason)
		}
	}
	if len(r.Manual) > 0 {
		p("\n[needs a human] these still mention bd and are NOT edited:")
		for _, line := range r.Manual {
			p("  - %s", line)
		}
	}
	if len(r.Notes) > 0 {
		p("\n[notes]")
		for _, n := range r.Notes {
			p("  - %s", n)
		}
	}
	if r.Mode == "dry-run" {
		p("\ndry-run: no files written; .beads/ is never modified. Re-run with --apply.")
		return b.String()
	}
	if r.BackupDir == "" {
		p("\nrefused: nothing was written")
		return b.String()
	}
	p("\n[applied]")
	for _, w := range r.Written {
		p("  wrote %s", w)
	}
	p("  backups of the rewritten files: %s", r.BackupDir)
	if r.KeptIssues > 0 {
		p("  %d issue(s) kept their newer local version", r.KeptIssues)
	}
	if r.PrimeLimit > 0 {
		p("  %d memories: set prime.memory_summary_limit=%d in .gofer/tracker/config.yaml so the session prime stays short (recall with `gofer memory ls <kw>`)", r.Memories, r.PrimeLimit)
	}
	if r.SkippedMem > 0 {
		p("  %d memory key(s) already existed and were kept", r.SkippedMem)
	}
	if v := r.Verified; v != nil {
		p("  verified: issues %d/%d match the source, memories %d/%d, .beads/issues.jsonl unchanged=%v", v.IssuesMatching, r.Issues, v.MemoriesMatch, r.Memories, v.BeadsUntouched)
	}
	p("  .beads/ was left in place (archive); commit .gofer/tracker/ and the edited files when ready")
	return b.String()
}

func countList(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

func idList(ids []string, format string) string {
	if len(ids) == 0 {
		return ""
	}
	shown := ids
	if len(shown) > 6 {
		shown = shown[:6]
	}
	text := strings.Join(shown, ", ")
	if len(ids) > len(shown) {
		text += ", ..."
	}
	return fmt.Sprintf(format, text)
}

func relTo(root, path string) string { return rel(root, path) }
