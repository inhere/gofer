package bdmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var bdMentionRe = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_])(bd|beads)([^A-Za-z0-9_]|$)|\.beads`)

// scanManual lists places that still talk about bd after the managed blocks
// are gone. None of them is edited: they are for a human to rewrite.
// `after` maps a file name to its planned new content when the migration edits it.
func scanManual(root string, after map[string]string) []string {
	var out []string
	files := []string{"CLAUDE.md", "AGENTS.md", "workspace.md"}
	// one level of `@file.md` imports from CLAUDE.md / AGENTS.md
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
	}
	read := func(name string) (string, bool) {
		if body, ok := after[name]; ok {
			return body, true
		}
		b, err := os.ReadFile(filepath.Join(root, name))
		return string(b), err == nil
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		body, ok := read(name)
		if !ok {
			continue
		}
		for _, line := range strings.Split(body, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "@") && strings.HasSuffix(t, ".md") && !strings.ContainsAny(t, " \t") {
				if imp := strings.TrimPrefix(t, "@"); !seen[imp] && !filepath.IsAbs(imp) && !strings.Contains(imp, "..") {
					seen[imp] = true
					files = append(files, imp)
				}
			}
		}
	}
	for _, name := range files {
		body, ok := read(name)
		if !ok {
			continue
		}
		n := 0
		for i, line := range strings.Split(body, "\n") {
			if bdMentionRe.MatchString(line) {
				if n++; n <= 25 {
					out = append(out, fmt.Sprintf("%s:%d: %s", name, i+1, clip(strings.TrimSpace(line), 110)))
				}
			}
		}
		if n > 25 {
			out = append(out, fmt.Sprintf("%s: ... %d more lines mention bd", name, n-25))
		}
	}
	// permission rules and other settings that still allow / call bd
	for _, name := range []string{".claude/settings.local.json", ".claude/settings.json", ".codex/config.toml"} {
		body, ok := read(name)
		if !ok {
			continue
		}
		var hits []string
		for _, line := range strings.Split(body, "\n") {
			if bdMentionRe.MatchString(line) {
				hits = append(hits, clip(strings.TrimSpace(line), 90))
			}
		}
		if len(hits) > 0 {
			out = append(out, fmt.Sprintf("%s: %d line(s) mention bd, e.g. %s", name, len(hits), hits[0]))
		}
	}
	// bd-specific skill / command directories
	for _, dir := range []string{".agents/skills", ".claude/skills", ".claude/commands", ".codex/skills"} {
		entries, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name()), "beads") || e.Name() == "bd" {
				out = append(out, fmt.Sprintf("%s/%s: a bd skill/command directory; remove it by hand if unused", dir, e.Name()))
			}
		}
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
