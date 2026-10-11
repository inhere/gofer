package config

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"
)

// DefaultSplitKeys are the sections `gofer config split` moves out by default: the
// two that usually make up most of a server config.
var DefaultSplitKeys = []string{"agents", "projects"}

// SplitResult describes what SplitConfig did (or, for a dry run, would do).
type SplitResult struct {
	Main    string            // the main config file
	Backup  string            // backup of the main file ("" for a dry run)
	Files   map[string]string // section -> new fragment file (absolute)
	Skipped map[string]string // section -> why it was not moved
	Include []string          // the main file's include list afterwards
	NewMain string            // the main file's new text
}

// SplitConfig moves top-level sections of the main config into one file each,
// <dir>/<section>.yaml (dir relative to the main file), and makes the main file
// include them (gofer-arsj). The text is MOVED, not re-rendered: a section's
// comments, key order and blank lines travel with it, and the comments above it go
// along — except a header above the first key of the file, which stays in the main
// file. The main file is backed up first; an existing target file is never
// overwritten. Afterwards the config is loaded again and its effective content
// compared with the original: any difference restores the main file, removes the new
// files and fails.
func SplitConfig(mainPath string, keys []string, dir string, dryRun bool) (SplitResult, error) {
	abs, err := filepath.Abs(mainPath)
	if err != nil {
		return SplitResult{}, err
	}
	if dir == "" {
		dir = "config.d"
	}
	if len(keys) == 0 {
		keys = DefaultSplitKeys
	}
	res := SplitResult{Main: abs, Files: map[string]string{}, Skipped: map[string]string{}}
	orig, err := os.ReadFile(abs)
	if err != nil {
		return res, err
	}
	before, _, err := Load(abs)
	if err != nil {
		return res, fmt.Errorf("load %s before splitting: %w", abs, err)
	}
	beforeText, err := canonicalText(before)
	if err != nil {
		return res, err
	}

	blocks, err := topBlocks(orig)
	if err != nil {
		return res, fmt.Errorf("parse %s: %w", abs, err)
	}
	lines := splitLines(string(orig))
	covered := 0
	for _, b := range blocks {
		covered += len(b.lines)
	}
	prefix := append([]string(nil), lines[:len(lines)-covered]...)

	move := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if k == includeKey {
			res.Skipped[k] = "include stays in the main file"
			continue
		}
		if !slices.ContainsFunc(blocks, func(b block) bool { return b.key == k }) {
			if f := before.SectionFile(k); f != "" && f != abs {
				res.Skipped[k] = "already in " + f
			} else {
				res.Skipped[k] = "not in the main file"
			}
			continue
		}
		target := filepath.Join(filepath.Dir(abs), filepath.FromSlash(dir), k+".yaml")
		if _, err := os.Stat(target); err == nil {
			return res, fmt.Errorf("%s already exists; move it away or pick another --dir", target)
		}
		move[k] = true
		res.Files[k] = target
	}
	if len(move) == 0 {
		return res, fmt.Errorf("nothing to split (%s)", describeSkips(res.Skipped))
	}

	// Build the fragments and the remaining main text.
	pattern := path.Join(filepath.ToSlash(dir), "*.yaml")
	include := append([]string(nil), before.Include...)
	if !slices.Contains(include, pattern) {
		include = append(include, pattern)
	}
	res.Include = include
	fragments := map[string]string{}
	var mainLines []string
	mainLines = append(mainLines, prefix...)
	for i, b := range blocks {
		if b.key == includeKey {
			continue // re-emitted below with the new entry
		}
		if !move[b.key] {
			mainLines = append(mainLines, b.lines...)
			continue
		}
		body := b.lines
		if i == 0 && len(b.head) > 0 {
			// A comment above the very first key is the file's header: keep it.
			mainLines = append(mainLines, b.head...)
			mainLines = append(mainLines, "")
			body = b.lines[len(b.head):]
		}
		fragments[b.key] = strings.TrimRight(strings.Join(body, "\n"), "\n") + "\n"
	}
	res.NewMain = insertInclude(mainLines, include)

	if dryRun {
		return res, nil
	}
	stamp := time.Now().Format("20060102-150405")
	res.Backup = abs + ".bak-" + stamp
	if err := os.WriteFile(res.Backup, orig, 0o644); err != nil {
		return res, fmt.Errorf("backup %s: %w", abs, err)
	}
	var created []string
	rollback := func(cause error) (SplitResult, error) {
		for _, f := range created {
			_ = os.Remove(f)
		}
		_ = os.WriteFile(abs, orig, 0o644)
		return res, cause
	}
	for k, target := range res.Files {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return rollback(err)
		}
		if err := os.WriteFile(target, []byte(fragments[k]), 0o644); err != nil {
			return rollback(err)
		}
		created = append(created, target)
	}
	if err := os.WriteFile(abs, []byte(res.NewMain), 0o644); err != nil {
		return rollback(err)
	}
	after, _, err := Load(abs)
	if err != nil {
		return rollback(fmt.Errorf("the split config does not load (restored): %w", err))
	}
	afterText, err := canonicalText(after)
	if err != nil {
		return rollback(err)
	}
	if afterText != beforeText {
		return rollback(fmt.Errorf("the split config is not equivalent to the original (restored)"))
	}
	return res, nil
}

// insertInclude renders the include list into the main text: in place of an existing
// include block, else right before the first top-level key (after a file header).
func insertInclude(mainLines []string, include []string) string {
	rendered := []string{"include:"}
	for _, p := range include {
		rendered = append(rendered, "  - "+p)
	}
	// An old include block was dropped from mainLines; the new one goes right above
	// the first top-level key — above the comment lines glued to it, below a file
	// header that a blank line separates from the key.
	ls := strings.Split(strings.Join(mainLines, "\n"), "\n")
	at := len(ls)
	for i, l := range ls {
		if l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "---") {
			at = i
			break
		}
	}
	for at > 0 && strings.HasPrefix(ls[at-1], "#") {
		at--
	}
	out := append([]string{}, ls[:at]...)
	out = append(out, rendered...)
	out = append(out, "")
	out = append(out, ls[at:]...)
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n"
}

// canonicalText renders the operator-owned content of cfg for an equivalence check,
// without the include list itself (which a split changes by design).
func canonicalText(cfg *Config) (string, error) {
	c := CanonicalConfig(cfg)
	c.Include = nil
	b, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(b)), nil
}

func describeSkips(skips map[string]string) string {
	parts := make([]string, 0, len(skips))
	for k, why := range skips {
		parts = append(parts, k+": "+why)
	}
	slices.Sort(parts)
	return strings.Join(parts, "; ")
}
