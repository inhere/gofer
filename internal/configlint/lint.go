// Package configlint finds config keys that can be deleted without changing what
// gofer actually runs with (`gofer config lint`, gofer-arsj). The test for every key
// is empirical: delete it, load the config again through the real loader and the
// real agent defaults, and compare the effective result. No separate rule table is
// kept, so the verdicts follow whatever the defaults are in this build.
package configlint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// Finding kinds.
const (
	KindRemove  = "remove"  // the key can be deleted (`--fix` deletes it)
	KindExtends = "extends" // the agent can be rewritten with extends: builtin (hint only)
	KindUnknown = "unknown" // gofer does not know the key and ignores it (typo?); never fixed
)

// Finding is one report line.
type Finding struct {
	File       string // absolute path
	Line       int    // 1-based line of the key
	Path       string // dotted key path, e.g. agents.claude.detect
	Kind       string
	Reason     string
	Suggestion string // KindExtends: the rewritten agent block
	Lines      int    // lines saved
}

// Report is the result of Lint.
type Report struct {
	Main     string
	Files    []string
	Findings []Finding
}

// Removable counts the KindRemove findings and the lines they free.
func (r Report) Removable() (keys, lines int) {
	for _, f := range r.Findings {
		if f.Kind == KindRemove {
			keys++
			lines += f.Lines
		}
	}
	return keys, lines
}

// session is one lint run: the files as currently edited in memory and the
// effective config they must keep producing.
type session struct {
	main   string
	files  []string
	text   map[string][]string // file -> lines (current, in memory)
	target string              // effective config of the original files
}

func newSession(mainPath string) (*session, error) {
	abs, err := filepath.Abs(mainPath)
	if err != nil {
		return nil, err
	}
	cfg, _, err := config.Load(abs)
	if err != nil {
		return nil, err
	}
	files := cfg.SourceFiles()
	if len(files) == 0 {
		files = []string{abs}
	}
	s := &session{main: abs, files: files, text: map[string][]string{}}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s.text[f] = strings.Split(string(b), "\n")
	}
	if s.target, err = effective(cfg); err != nil {
		return nil, err
	}
	return s, nil
}

// equivalent reports whether the config with file's text replaced by lines still has
// the original effective content (a load error counts as different).
func (s *session) equivalent(file string, lines []string) bool {
	over := map[string][]byte{}
	for f, ls := range s.text {
		over[f] = []byte(strings.Join(ls, "\n"))
	}
	over[file] = []byte(strings.Join(lines, "\n"))
	cfg, err := config.LoadWithOverrides(s.main, over)
	if err != nil {
		return false
	}
	got, err := effective(cfg)
	return err == nil && got == s.target
}

// effective renders what gofer runs with: every agent resolved through the same
// defaults the runtime applies (agent.ResolveAgent) with its version probe spelled
// out, project subdirectories resolved, and the include list left out.
func effective(cfg *config.Config) (string, error) {
	c := cfg.Clone()
	c.Include = nil
	c.Agents = agent.EffectiveAgents(cfg)
	projects := make(map[string]config.ProjectConfig, len(cfg.Projects))
	for k, p := range cfg.Projects {
		p.ExchangeSubdir = cfg.ResolvedExchangeSubdir(p)
		p.ResultSubdir = cfg.ResolvedResultSubdir(p)
		projects[k] = p
	}
	c.Projects = projects
	b, err := json.Marshal(c)
	return string(b), err
}

// candidate reports whether a key is worth testing: settings one level inside a
// top-level section, agent and project entries, and the fields of those entries.
func candidate(n node) bool {
	switch {
	case n.depth == 2:
		return true
	case n.depth == 3:
		return strings.HasPrefix(n.path, "agents.") || strings.HasPrefix(n.path, "projects.")
	}
	return false
}

// Lint reports the keys of the config at mainPath (and its included files) that can
// be deleted without changing the effective config, and the agents that could be
// written as `extends: builtin`. It never writes a file.
func Lint(mainPath string) (Report, error) {
	s, err := newSession(mainPath)
	if err != nil {
		return Report{}, err
	}
	r := Report{Main: s.main, Files: s.files}
	for _, f := range s.files {
		lines := s.text[f]
		removedUnder := ""
		for _, n := range scanNodes(lines) {
			if removedUnder != "" && strings.HasPrefix(n.path, removedUnder+".") {
				continue
			}
			if !knownPath(n.path) {
				// Reported, never deleted: an unknown key is more often a misspelt
				// one than dead weight, and deleting it would hide the mistake.
				removedUnder = n.path
				r.Findings = append(r.Findings, Finding{
					File: f, Line: n.key + 1, Path: n.path, Kind: KindUnknown,
					Reason: "unknown key: gofer ignores it (misspelt?)", Lines: n.end - n.start,
				})
				continue
			}
			if !candidate(n) || !s.equivalent(f, without(lines, n.start, n.end)) {
				continue
			}
			removedUnder = n.path
			r.Findings = append(r.Findings, Finding{
				File: f, Line: n.key + 1, Path: n.path, Kind: KindRemove,
				Reason: removeReason(n.path), Lines: n.end - n.start,
			})
		}
		r.Findings = append(r.Findings, s.extendsHints(f, lines)...)
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return r, nil
}

func removeReason(path string) string {
	if strings.HasSuffix(path, ".detect") {
		return "same as the default version probe (<command> --version)"
	}
	return "same as the default: deleting it changes nothing"
}

// FixResult describes what Fix changed.
type FixResult struct {
	Removed []Finding
	Backups []string
}

// Fix deletes every removable key, one at a time on the current text, keeping a
// deletion only while the effective config stays the same (two keys can each be
// redundant alone yet not together). Each changed file is backed up first; the result
// is loaded again and compared before Fix returns — any difference restores all files.
func Fix(mainPath string) (FixResult, error) {
	r, err := Lint(mainPath)
	if err != nil {
		return FixResult{}, err
	}
	s, err := newSession(mainPath)
	if err != nil {
		return FixResult{}, err
	}
	orig := map[string][]string{}
	for f, ls := range s.text {
		orig[f] = ls
	}
	var res FixResult
	// Bottom-up per file so earlier line numbers stay valid.
	findings := append([]Finding(nil), r.Findings...)
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line > findings[j].Line
	})
	for _, f := range findings {
		if f.Kind != KindRemove {
			continue
		}
		lines := s.text[f.File]
		var target *node
		for _, n := range scanNodes(lines) {
			if n.path == f.Path && n.key == f.Line-1 {
				n := n
				target = &n
				break
			}
		}
		if target == nil {
			continue
		}
		next := without(lines, target.start, target.end)
		if !s.equivalent(f.File, next) {
			continue
		}
		s.text[f.File] = next
		res.Removed = append(res.Removed, f)
	}
	if len(res.Removed) == 0 {
		return res, nil
	}
	// A section whose every key went is left as a bare `log:`; drop such empty keys
	// too (still only when that keeps the config equivalent).
	for _, f := range s.files {
		for changed := true; changed; {
			changed = false
			lines := s.text[f]
			nodes := scanNodes(lines)
			for i := len(nodes) - 1; i >= 0; i-- {
				n := nodes[i]
				if n.end-n.key != 1 || !strings.HasSuffix(strings.TrimSpace(lines[n.key]), ":") {
					continue
				}
				if next := without(lines, n.start, n.end); s.equivalent(f, next) {
					s.text[f] = next
					changed = true
					break
				}
			}
		}
	}
	stamp := time.Now().Format("20060102-150405")
	var written []string
	restore := func(cause error) (FixResult, error) {
		for _, f := range written {
			_ = os.WriteFile(f, []byte(strings.Join(orig[f], "\n")), 0o644)
		}
		return res, cause
	}
	for _, f := range s.files {
		if strings.Join(s.text[f], "\n") == strings.Join(orig[f], "\n") {
			continue
		}
		backup := f + ".bak-" + stamp
		if err := os.WriteFile(backup, []byte(strings.Join(orig[f], "\n")), 0o644); err != nil {
			return restore(err)
		}
		res.Backups = append(res.Backups, backup)
		if err := os.WriteFile(f, []byte(strings.Join(s.text[f], "\n")), 0o644); err != nil {
			return restore(err)
		}
		written = append(written, f)
	}
	after, _, err := config.Load(s.main)
	if err != nil {
		return restore(fmt.Errorf("the fixed config does not load (restored): %w", err))
	}
	if got, err := effective(after); err != nil || got != s.target {
		return restore(fmt.Errorf("the fixed config is not equivalent to the original (restored)"))
	}
	return res, nil
}
