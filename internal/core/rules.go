package core

import (
	"fmt"
	"path/filepath"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/rule"
)

// rules.go is JOB-06①'s hub side of the rule-library seam: the ONE place that knows
// both the job package's contract (job.RuleLibrary) and the rule store, exactly like
// skill.go does for the skill library (G022 — neither package imports the other).
//
//   - Get answers the digest the job row records.
//   - Body answers the text that is injected into the prompt.
//
// There is no mounting and no staging here, and that is the whole difference from a
// skill: a rule is TEXT the submitting machine puts into the prompt, not files a
// worker has to receive.
type hubRuleLibrary struct {
	store *rule.Store
}

// Rules returns the JOB-06① rule library store (nil when the library was never
// built). It is the SAME store the job seam resolves bindings against and the one
// serve injects into httpapi (SetRules), so the CLI/HTTP surface and the injection
// path can never disagree about what a rule says.
func (c *Core) Rules() *rule.Store {
	if c == nil || c.ruleLib == nil {
		return nil
	}
	return c.ruleLib.store
}

// buildRuleLibrary opens the library at <config-dir>/rules (the same directory the
// `agent rule set` writes land in, next to the rest of the operator's config) over
// the metadata store's rules table.
func buildRuleLibrary(repo *jobstore.Store) (*hubRuleLibrary, error) {
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	st, err := rule.NewStore(filepath.Join(cfgDir, "rules"), repo)
	if err != nil {
		return nil, err
	}
	return &hubRuleLibrary{store: st}, nil
}

// Get implements job.RuleLibrary. A read error is reported as "not present": a
// submit naming that rule is then rejected instead of running without it.
func (h hubRuleLibrary) Get(name string) (job.RuleInfo, bool) {
	r, ok, err := h.store.Get(name)
	if err != nil || !ok {
		return job.RuleInfo{}, false
	}
	return job.RuleInfo{Name: r.Name, Description: r.Description, SHA256: r.SHA256, Size: r.Size}, true
}

// Body implements job.RuleLibrary: the rule's text with its frontmatter stripped —
// what the agent is meant to read (the metadata head is for the library, not the
// prompt).
func (h hubRuleLibrary) Body(name string) (string, error) { return h.store.Body(name) }
