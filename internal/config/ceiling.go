// Caller scope ceilings (上限): the operator-declared upper bound of what ONE caller
// (one token) may reach, and the load-time checks that keep a ceiling honest.
//
// A ceiling is a subtractive layer, never a grant: a caller's effective range is its
// permissions ∩ its ceiling. An absent ceiling (`ceiling` key missing, or nil) restricts
// nothing, which is why every predicate here allows everything on a nil receiver — the
// caller of these functions is expected to ask `CallerCeiling(id)` first and treat
// ok=false as "no ceiling".
//
// The design is docs/design/2026-10-18-caller-scope-ceilings-design.md; this file owns
// the model and the pure predicates, the job layer owns admission, and httpapi owns the
// route/read-write judgment.
package config

import (
	"fmt"
	"path"
	"slices"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// CallerCeiling is one caller's upper bound. Every dimension is a glob list (path.Match
// semantics, `*` does not cross `/`) or a tri-state switch:
//
//   - Projects, Agents, Workers: EMPTY = the dimension is not restricted. A non-empty
//     list requires the value to match one of its patterns; the empty value matches only
//     a pattern that matches the empty string (e.g. `*`), so a request that names
//     nothing in a restricted dimension is refused (fail closed).
//   - Runners: EMPTY = every runner EXCEPT the built-in in-process one, i.e. remote
//     workers only. The machine runner (local / server) must be listed explicitly —
//     a job on it runs on the server host and its agent can read the server's config
//     and tokens, so a ceiling is not a security boundary for a caller that has it.
//     Entries are compared after NormalizeRunnerName, so either spelling of the built-in
//     runner works.
//   - AllowExec / GlobalMemory / ReadOnly: nil (the key absent) resolves to the STRICT
//     side of the design — exec off, global memory off, read-only off. The first two are
//     restrictions, so `*bool` is what distinguishes "the operator left it out" from an
//     explicit `false`; ReadOnly defaults to off because a ceiling is not a read-only
//     switch unless it says so.
//
// It is a pure value: no method mutates it, and Clone deep-copies it (cloneServer).
type CallerCeiling struct {
	// Projects are the project keys the caller may touch (glob).
	Projects []string `yaml:"projects,omitempty" json:"projects,omitempty"`
	// Runners are the runner keys the caller may submit to (glob, `local`/`server`
	// included). Empty = no machine runner (worker runners only).
	Runners []string `yaml:"runners,omitempty" json:"runners,omitempty"`
	// Workers are the workers the caller's jobs may be routed to (glob). Empty = any.
	Workers []string `yaml:"workers,omitempty" json:"workers,omitempty"`
	// Agents are the agents the caller may use (glob). Empty = any.
	Agents []string `yaml:"agents,omitempty" json:"agents,omitempty"`
	// AllowExec permits exec jobs (agent exec, a Cmd, a Verify argv, hooks) for this
	// caller. nil = not allowed.
	AllowExec *bool `yaml:"allow_exec,omitempty" json:"allow_exec,omitempty"`
	// GlobalMemory permits writing memory that is not scoped to one of the caller's
	// projects. nil = not allowed.
	GlobalMemory *bool `yaml:"global_memory,omitempty" json:"global_memory,omitempty"`
	// ReadOnly refuses every state-changing request of this caller. nil = not read-only.
	ReadOnly *bool `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

// clone returns a deep copy: the lists get their own backing arrays and the switches
// their own allocations, so a writer mutating the copy can never be observed by a
// concurrent reader of the original (the cloneServer contract).
func (c *CallerCeiling) clone() *CallerCeiling {
	if c == nil {
		return nil
	}
	out := *c
	out.Projects = slices.Clone(c.Projects)
	out.Runners = slices.Clone(c.Runners)
	out.Workers = slices.Clone(c.Workers)
	out.Agents = slices.Clone(c.Agents)
	out.AllowExec = clonePtr(c.AllowExec)
	out.GlobalMemory = clonePtr(c.GlobalMemory)
	out.ReadOnly = clonePtr(c.ReadOnly)
	return &out
}

// CallerCeiling returns the ceiling of callerID and whether it has one. A caller the
// configuration does not declare, and a declared caller without a `ceiling` block,
// both answer (nil, false) — "no ceiling" is the state every caller starts in.
//
// Like CallerConcurrencyLimit this is a pure lookup over the caller list (first match
// wins), so it can be read from a live config snapshot on every judgment.
func (sc *ServerConfig) CallerCeiling(id string) (*CallerCeiling, bool) {
	if sc == nil || id == "" {
		return nil, false
	}
	for i := range sc.Callers {
		if sc.Callers[i].ID != id {
			continue
		}
		if sc.Callers[i].Ceiling == nil {
			return nil, false
		}
		return sc.Callers[i].Ceiling, true
	}
	return nil, false
}

// CeilingCallers returns the ids of the callers that carry a ceiling, sorted. It is what
// the serve startup log reports ("N restricted caller(s)"), so an operator can see that
// limits are in force before trusting them.
func (sc *ServerConfig) CeilingCallers() []string {
	if sc == nil {
		return nil
	}
	var out []string
	for _, cc := range sc.Callers {
		if cc.ID != "" && cc.Ceiling != nil {
			out = append(out, cc.ID)
		}
	}
	slices.Sort(out)
	return out
}

// AllowsProject reports whether the ceiling admits project key p. A nil ceiling (or an
// empty Projects list) admits everything.
func (c *CallerCeiling) AllowsProject(p string) bool {
	if c == nil || len(c.Projects) == 0 {
		return true
	}
	return ceilingMatches(c.Projects, p)
}

// AllowsRunner reports whether the ceiling admits runner r. Both sides are normalized
// through NormalizeRunnerName, so `runners: [server]` and `runners: [local]` are the same
// declaration (G043: one normalizer for the two spellings of the built-in runner).
//
// An empty Runners list admits every runner EXCEPT the built-in in-process one (the
// machine runner must be spelled out); a non-empty list admits only what it matches, and
// the built-in runner only by name — a glob never matches it.
func (c *CallerCeiling) AllowsRunner(r string) bool {
	if c == nil {
		return true
	}
	name := NormalizeRunnerName(r)
	if len(c.Runners) == 0 {
		return !IsBuiltinLocalRunnerName(name)
	}
	local := IsBuiltinLocalRunnerName(name)
	for _, pattern := range c.Runners {
		pattern = NormalizeRunnerName(pattern)
		if pattern == name {
			return true
		}
		// The server's own runner is admitted only by naming it (local / server): a
		// glob such as "*" must not hand out a shell on the server host.
		if local {
			continue
		}
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}

// AllowsWorker reports whether the ceiling admits worker w. A nil ceiling (or an empty
// Workers list) admits everything.
func (c *CallerCeiling) AllowsWorker(w string) bool {
	if c == nil || len(c.Workers) == 0 {
		return true
	}
	return ceilingMatches(c.Workers, w)
}

// AllowsAgent reports whether the ceiling admits agent key a. A nil ceiling (or an empty
// Agents list) admits everything.
func (c *CallerCeiling) AllowsAgent(a string) bool {
	if c == nil || len(c.Agents) == 0 {
		return true
	}
	return ceilingMatches(c.Agents, a)
}

// ExecAllowed reports whether the caller may run exec jobs. A nil ceiling restricts
// nothing and answers true; a ceiling that does not set allow_exec answers false (the
// strict default of decision 3).
func (c *CallerCeiling) ExecAllowed() bool {
	if c == nil {
		return true
	}
	return c.AllowExec != nil && *c.AllowExec
}

// GlobalMemoryAllowed reports whether the caller may write global (not project-scoped)
// memory. A nil ceiling restricts nothing and answers true; a ceiling that does not set
// global_memory answers false.
func (c *CallerCeiling) GlobalMemoryAllowed() bool {
	if c == nil {
		return true
	}
	return c.GlobalMemory != nil && *c.GlobalMemory
}

// IsReadOnly reports whether every state-changing request of this caller is refused. A
// nil ceiling (or one that does not set read_only) answers false.
func (c *CallerCeiling) IsReadOnly() bool {
	if c == nil {
		return false
	}
	return c.ReadOnly != nil && *c.ReadOnly
}

// ceilingMatches reports whether value matches one of the glob patterns (path.Match
// semantics). A malformed pattern matches nothing: the load refuses those, so this is
// only a belt-and-braces guard for a config built in memory.
func ceilingMatches(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matched, err := path.Match(pattern, value); err == nil && matched {
			return true
		}
	}
	return false
}

// validateCallerCeilings holds every `server.callers[].ceiling` block to the bar the
// loader enforces at load, on validate and on a reload:
//
//   - every pattern must be a legal path.Match glob (a typo would silently match nothing
//     and turn a limit into a lock-out or, worse, read as no limit at all);
//   - a root administrator may NOT carry a ceiling: the design's root holds every
//     permission whatever the configuration says, so a ceiling there would be a limit
//     that silently does not apply. The root set is read from the same helpers authz.Derive
//     derives it from (RootAdminCallers / HasRootAdmin), so the two cannot disagree.
func validateCallerCeilings(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	roots := cfg.Server.RootAdminCallers()
	for i, cc := range cfg.Server.Callers {
		ceil := cc.Ceiling
		if ceil == nil {
			continue
		}
		owner := fmt.Sprintf("server.callers[%d]", i)
		if cc.ID != "" {
			owner = fmt.Sprintf("server.callers[%q]", cc.ID)
		}
		for _, dim := range []struct {
			key      string
			patterns []string
		}{
			{"projects", ceil.Projects},
			{"runners", ceil.Runners},
			{"workers", ceil.Workers},
			{"agents", ceil.Agents},
		} {
			for _, pattern := range dim.patterns {
				if strings.TrimSpace(pattern) == "" {
					return fmt.Errorf("%s.ceiling.%s: empty pattern", owner, dim.key)
				}
				if _, err := path.Match(pattern, "sample"); err != nil {
					return fmt.Errorf("%s.ceiling.%s: invalid pattern %q: %w", owner, dim.key, pattern, err)
				}
			}
		}
		if cc.ID != "" && slices.Contains(roots, cc.ID) {
			return fmt.Errorf("%s: caller %q is a root administrator (can_admin); a ceiling cannot apply to the root, "+
				"which holds every permission whatever the configuration says", owner, cc.ID)
		}
	}
	return nil
}

// callerCeilingWarnings reports the load-time notices the callers' ceilings produce,
// label naming the file holding the server section. There is one: a ceiling that lets
// the caller use the machine runner (local / server) is not a security boundary — a job
// on that runner executes on the server host, so its agent can read the server's config
// and every token. The warning is emitted through the ordinary load-warning channel
// (config.LoadWarnings → serve startup, `config info`, GET /v1/config).
func callerCeilingWarnings(cfg *Config, label string) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for _, cc := range cfg.Server.Callers {
		if cc.Ceiling == nil || !cc.Ceiling.AllowsRunner(BuiltinLocalRunner) {
			continue
		}
		name := cc.ID
		if name == "" {
			name = "(no id)"
		}
		out = append(out, fmt.Sprintf("%s: server.callers[%q].ceiling allows the machine runner (%s): "+
			"a job there runs on the server host and can read its config and tokens, so this ceiling is not a security boundary",
			label, name, BuiltinLocalRunner))
	}
	return out
}

// checkCeilingSubtree decodes every `ceiling` sub-tree of one config FILE strictly: an
// unknown key (a typo such as `projecs`) fails the load instead of being silently
// ignored, and a `ceiling:` that sets no field at all is refused (it reads as if the
// caller had been limited). label names the file for the error message.
//
// Only this sub-tree is strict — the rest of the document keeps the non-strict decode the
// loader documents (an operator's stray key must not brick a running server) — which is
// why the probe works on a plain-map decode of the raw bytes and then decodes JUST the
// ceiling mapping again with yaml.DisallowUnknownField. Values are not interpreted here:
// a document the typed decode already rejected is a no-op (the caller has the real error).
func checkCeilingSubtree(label string, raw []byte) error {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	server, ok := doc["server"].(map[string]any)
	if !ok {
		return nil
	}
	callers, ok := server["callers"].([]any)
	if !ok {
		return nil
	}
	for i, item := range callers {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		value, found := entry["ceiling"]
		if !found {
			continue
		}
		owner := fmt.Sprintf("server.callers[%d]", i)
		if id, ok := entry["id"].(string); ok && strings.TrimSpace(id) != "" {
			owner = fmt.Sprintf("server.callers[%q]", strings.TrimSpace(id))
		}
		body, ok := value.(map[string]any)
		if !ok {
			// Not a mapping at all (or an explicit null): the typed decode reports the type
			// error, and `ceiling:` with nothing under it is the empty ceiling below.
			if value == nil {
				return fmt.Errorf("%s.ceiling: empty ceiling: list what the caller may do, or remove the key", owner)
			}
			continue
		}
		if len(body) == 0 {
			return fmt.Errorf("%s.ceiling: empty ceiling: list what the caller may do, or remove the key", owner)
		}
		rendered, err := yaml.Marshal(body)
		if err != nil {
			continue
		}
		var strict CallerCeiling
		if err := yaml.UnmarshalWithOptions(rendered, &strict, yaml.DisallowUnknownField()); err != nil {
			return fmt.Errorf("%s.ceiling: %w (the ceiling block is decoded strictly, unlike the rest of the config)", owner, err)
		}
	}
	return nil
}
