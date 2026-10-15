package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

// memoryScopeOptions are the `memory scope` flags, resolved by the command layer.
type memoryScopeOptions struct {
	Apply         bool
	MinConfidence string
	Only          string
}

// memoryScopeOutput is the `memory scope` result (--json).
type memoryScopeOutput struct {
	// Suggestions are the proposals: all of them for --suggest, the ones actually
	// written for --apply.
	Suggestions []tracker.ScopeSuggestion `json:"suggestions"`
	// Applied lists the memory keys --apply rewrote.
	Applied []string `json:"applied,omitempty"`
	// BelowThreshold lists the keys --apply left alone because their evidence is weaker
	// than --min-confidence.
	BelowThreshold []string `json:"below_threshold,omitempty"`
	// Unknown lists the keys --only named that have no suggestion at all.
	Unknown []string `json:"unknown,omitempty"`
	// AlreadyScoped / Unresolved count the memories the scan left alone: they already have
	// a when.paths, or no evidence names a single subproject.
	AlreadyScoped int `json:"already_scoped"`
	Unresolved    int `json:"unresolved"`
}

// runMemoryScope is `gofer memory scope`: it suggests (or writes) the applicable path of
// every memory of this tracker that has none, so a workspace root shared by several
// checkouts stops injecting each checkout's memories into the others' jobs (gofer-opv5).
func runMemoryScope(c *gcli.Command, s *tracker.Store, opts memoryScopeOptions, asJSON bool) error {
	if s.RepoRoot() == "" {
		return fmt.Errorf("memory scope works on a repository tracker (<root>/.gofer/tracker)")
	}
	min := strings.ToLower(strings.TrimSpace(opts.MinConfidence))
	if min == "" {
		min = tracker.ScopeConfidenceHigh
	}
	if min != tracker.ScopeConfidenceHigh && min != tracker.ScopeConfidenceMedium {
		return fmt.Errorf("invalid --min-confidence %q (high|medium)", opts.MinConfidence)
	}
	items, err := s.ReadMemories()
	if err != nil {
		return err
	}
	scoped := 0
	for _, item := range items {
		if item.When != nil && len(item.When.Paths) > 0 {
			scoped++
		}
	}
	suggestions, err := s.SuggestMemoryScopes(memoryScopeCwdLookup(s))
	if err != nil {
		return err
	}
	out := memoryScopeOutput{AlreadyScoped: scoped, Unresolved: len(items) - scoped - len(suggestions)}

	only, err := parseMemoryScopeOnly(opts.Only)
	if err != nil {
		return err
	}
	kept := make([]tracker.ScopeSuggestion, 0, len(suggestions))
	for _, sug := range suggestions {
		if only != nil {
			if _, ok := only[sug.Key]; !ok {
				continue
			}
			delete(only, sug.Key)
		}
		kept = append(kept, sug)
	}
	// A key --only named that the scan has no suggestion for (already scoped, or no
	// evidence): say so instead of silently doing nothing.
	for key := range only {
		out.Unknown = append(out.Unknown, key)
	}
	sort.Strings(out.Unknown)

	if !opts.Apply {
		out.Suggestions = kept
		if asJSON {
			return printTrackerJSON(c, out)
		}
		printMemoryScopeSuggestions(c, kept, out)
		return nil
	}

	selected := make([]tracker.ScopeSuggestion, 0, len(kept))
	for _, sug := range kept {
		if tracker.ScopeConfidenceRank(sug.Confidence) >= tracker.ScopeConfidenceRank(min) {
			selected = append(selected, sug)
			continue
		}
		out.BelowThreshold = append(out.BelowThreshold, sug.Key)
	}
	if !asJSON {
		for _, sug := range selected {
			c.Printf("set %s -> %s（%s）\n", sug.Key, strings.Join(sug.Paths, ","), sug.Basis)
		}
	}
	changed, err := s.ApplyMemoryScopePaths(selected)
	if err != nil {
		return err
	}
	for _, item := range changed {
		out.Applied = append(out.Applied, item.Key)
	}
	out.Suggestions = selected
	tryAutoSync(c, s)
	if asJSON {
		return printTrackerJSON(c, out)
	}
	printMemoryScopeApplyResult(c, out)
	return nil
}

func printMemoryScopeSuggestions(c *gcli.Command, suggestions []tracker.ScopeSuggestion, out memoryScopeOutput) {
	c.Printf("%d suggestions: %d memories already scoped, %d with no subproject evidence\n",
		len(suggestions), out.AlreadyScoped, out.Unresolved)
	if len(suggestions) == 0 {
		if len(out.Unknown) > 0 {
			c.Printf("no suggestion for: %s\n", strings.Join(out.Unknown, ", "))
		}
		return
	}
	keyWidth, pathWidth := len("KEY"), len("WHEN.PATHS")
	for _, sug := range suggestions {
		keyWidth = max(keyWidth, len(sug.Key))
		pathWidth = max(pathWidth, len(strings.Join(sug.Paths, ",")))
	}
	c.Printf("%-*s  %-*s  %s\n", keyWidth, "KEY", pathWidth, "WHEN.PATHS", "依据 / 置信度")
	for _, sug := range suggestions {
		paths := strings.Join(sug.Paths, ",")
		c.Printf("%-*s  %-*s  [%s] %s\n", keyWidth, sug.Key, pathWidth, paths, tracker.ScopeConfidenceLabel(sug.Confidence), sug.Basis)
	}
	c.Printf("apply: gofer memory scope --apply [--min-confidence medium] [--only <key>,<key>]\n")
	if len(out.Unknown) > 0 {
		c.Printf("no suggestion for: %s\n", strings.Join(out.Unknown, ", "))
	}
}

func printMemoryScopeApplyResult(c *gcli.Command, out memoryScopeOutput) {
	c.Printf("applied %d memories (%d already scoped, %d with no evidence)\n", len(out.Applied), out.AlreadyScoped, out.Unresolved)
	if len(out.BelowThreshold) > 0 {
		c.Printf("skipped %d weaker suggestions: %s（--min-confidence medium 也写入）\n",
			len(out.BelowThreshold), strings.Join(out.BelowThreshold, ", "))
	}
	if len(out.Unknown) > 0 {
		c.Printf("no suggestion for: %s\n", strings.Join(out.Unknown, ", "))
	}
}

// parseMemoryScopeOnly parses --only into a set ("", = nil).
func parseMemoryScopeOnly(value string) (map[string]bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	keys := make(map[string]bool)
	for _, key := range strings.Split(value, ",") {
		if key = strings.TrimSpace(key); key != "" {
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("--only needs at least one memory key")
	}
	return keys, nil
}

// memoryScopeCwdLookup resolves a memory's source (job:<id> / session:<id>) to the working
// directory it was written from. The server is the only record of it, so the lookup is
// nil — the source evidence is simply unavailable — without a project key or a reachable
// server, and a failed lookup is not an error: the other evidence still applies.
func memoryScopeCwdLookup(s *tracker.Store) func(string) (string, bool) {
	cfg, err := s.ReadConfig()
	if err != nil || strings.TrimSpace(cfg.ProjectKey) == "" {
		return nil
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return nil
	}
	return func(source string) (string, bool) {
		kind, id, ok := strings.Cut(strings.TrimSpace(source), ":")
		if !ok || strings.TrimSpace(id) == "" {
			return "", false
		}
		switch kind {
		case "job":
			res, err := cli.GetJob(strings.TrimSpace(id))
			if err != nil || strings.TrimSpace(res.Cwd) == "" {
				return "", false
			}
			return res.Cwd, true
		case "session":
			detail, err := cli.GetSession(strings.TrimSpace(id), 0, "")
			if err != nil || strings.TrimSpace(detail.Session.Cwd) == "" {
				return "", false
			}
			return detail.Session.Cwd, true
		}
		return "", false
	}
}
