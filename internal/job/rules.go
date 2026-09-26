package job

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// rules.go is JOB-06①'s job-side half: a rule is a short, MANDATORY piece of
// discipline the server owns and injects at the top of the job's prompt — unlike a
// skill (skills.go), which is optional reading material the agent may open.
//
//   - Resolution happens ONCE, in Submit, from the same config snapshot as every
//     other admitted value (the four-level union + the project's `.gofer/RULES.md`).
//   - The TEXT is injected into req.Prompt here, at submit, so it rides request_json
//     (auditable: "what exactly was this agent told to obey?"), the Forward and the
//     executing machine alike — no new wire field, and every agent (claude, codex,
//     omp, acp…) sees the same section in the same place (design 决策 1: not
//     SystemInject, which only two agents have).
//   - The section is rendered by the SUBMITTING machine for every job, including a
//     remote one: the receiving gofer keeps a prompt that already carries the section
//     (hasRulesSection) and never re-decides it. A rerun (RebuildJob) drops the stale
//     section so the CURRENT rules are re-read.
//   - The skills manifest is inserted AFTER this section, never above it: rules are
//     the frame, the skills list is content (see insertSkillsManifest).
//
// The library itself lives behind RuleLibrary — this package never imports
// internal/rule (G022).

const (
	// rulesPromptHeader opens the injected section. It is a fixed literal because the
	// machine that later inserts the skills manifest recognises it STRUCTURALLY (a
	// prefix match, no markdown parsing) — and because a human reading request_json
	// should see at a glance where gofer's rules start.
	rulesPromptHeader = "## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）"
	// rulesPromptEnd closes the section. It is an HTML comment so it renders as
	// nothing in every markdown viewer while staying a line we can find byte-exactly.
	rulesPromptEnd = "<!-- gofer:rules-end -->"
	// projectRulesFile is the repository-local rule file (design §一.2): a project's
	// own discipline travels with the checkout (reviewed in the same PR as the code it
	// constrains) and needs no registration. It is read from THIS machine's view of the
	// project root (G002).
	projectRulesFile = ".gofer/RULES.md"
	// ProjectRulePrefix names that auto-included rule: `project:<key>`.
	ProjectRulePrefix = "project:"
	// ruleUnreachable is the job.rules_skipped reason for a project file this machine
	// could not read (a worker's tree from the hub's point of view, a permissions
	// problem). The job still runs: a rule this machine cannot see is not a reason to
	// refuse work it can do.
	ruleUnreachable = "project_file_unreachable"
	// rulesLargestShown is how many offenders the over-limit error names. Three is
	// enough to point at the problem rule without turning the 400 into a wall of text.
	rulesLargestShown = 3
)

// RuleRef is one rule as a job RECORDS it: the name plus the sha256 of the text that
// was injected. It is what `job show` / the web detail prints and what makes "which
// version of house-rules did this run carry?" answerable after the library changed.
type RuleRef struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256,omitempty"`
}

// RuleInfo is what the job package needs to know about one library entry: the digest
// the row records. The text is fetched separately (RuleLibrary.Body) because only the
// resolving machine needs the bytes.
type RuleInfo struct {
	Name        string
	Description string
	SHA256      string
	Size        int64
}

// RuleLibrary is the server's rule asset library as this package needs it.
// *rule.Store-backed adapter is wired at assemble time (core.Build).
type RuleLibrary interface {
	// Get returns one entry's metadata; ok=false means the name is unknown (a submit
	// naming it is rejected rather than run without its rules).
	Get(name string) (RuleInfo, bool)
	// Body returns the text to INJECT: the rule file with its frontmatter stripped.
	Body(name string) (string, error)
}

// SetRuleLibrary installs the rule-library seam. Without one, a job that binds a rule
// fails validation with an explanation instead of running without it.
func (s *Service) SetRuleLibrary(l RuleLibrary) { s.rules = l }

// resolveRules expands the binding levels into the job's final rule set, IN PLACE on
// the request: the union (config.EffectiveRules), the project's `.gofer/RULES.md`,
// the size cap, and the rendered section prepended to req.Prompt.
//
// It runs in Submit BEFORE validate, so an unknown rule name or an over-limit set is
// a rejected submit that creates nothing.
func (s *Service) resolveRules(cfg *config.Config, req *JobRequest, _ bool) error {
	if req.RulesResolved {
		// A resume (the session already carries the rules) or a job re-entering from
		// the machine the hub dispatched to (its prompt already carries the section):
		// nothing to decide here.
		return nil
	}
	if hasRulesSection(req.Prompt) {
		// The prompt already opens with an injected section but this request names no
		// rules of its own: it came from another gofer that already decided (a
		// peer-http re-submit of this hub's own prompt). Keep that decision — replacing
		// it would drop the discipline the submitting side chose.
		if len(req.Rules) == 0 {
			return nil
		}
		// A rerun of a persisted request DOES carry its names: drop the stale section
		// (RebuildJob also strips it) and render the current rules below.
		req.Prompt = stripRulesSection(req.Prompt)
	}

	agentType := ""
	if ac, ok := agent.ResolveAgent(cfg, req.Agent); ok {
		agentType = ac.Type
	}
	names := cfg.EffectiveRules(req.ProjectKey, req.Agent, req.Rules, req.NoRules, agentType)
	if len(names) == 0 {
		return nil
	}
	if s.rules == nil {
		return fmt.Errorf("%w: rules are not available on this server", ErrInvalidRequest)
	}

	refs := make([]RuleRef, 0, len(names)+1)
	bodies := make(map[string]string, len(names)+1)
	var total int64
	for _, name := range names {
		info, ok := s.rules.Get(name)
		if !ok {
			return fmt.Errorf("%w: unknown rule %q", ErrInvalidRequest, name)
		}
		body, err := s.rules.Body(name)
		if err != nil {
			return fmt.Errorf("%w: read rule %q: %s", ErrInvalidRequest, name, err.Error())
		}
		refs = append(refs, RuleRef{Name: name, SHA256: info.SHA256})
		bodies[name] = body
		total += int64(len(body))
	}

	// The repository's own file, AFTER the bound rules (design §一.2). It is one more
	// rule, named `project:<key>`; a directory without one contributes nothing (the
	// common case), and a file this machine cannot read is skipped with an event
	// rather than failing a job that can still run.
	if proj, ok := cfg.Projects[req.ProjectKey]; ok && strings.TrimSpace(req.ProjectKey) != "" {
		p := filepath.Join(cfg.ExecPath(proj), filepath.FromSlash(projectRulesFile))
		data, err := os.ReadFile(p)
		switch {
		case err == nil && len(bytes.TrimSpace(data)) > 0:
			name := ProjectRulePrefix + req.ProjectKey
			body := ruleBodyOf(data)
			sum := sha256.Sum256(data)
			refs = append(refs, RuleRef{Name: name, SHA256: hex.EncodeToString(sum[:])})
			bodies[name] = body
			total += int64(len(body))
		case err != nil && !errors.Is(err, os.ErrNotExist):
			req.rulesSkipped = ruleUnreachable
		}
	}

	limit := int64(cfg.EffectiveRulesMaxBytes())
	if total > limit {
		return fmt.Errorf("%w: rules total %d bytes, over server.rules_max_bytes (%d); largest: %s",
			ErrInvalidRequest, total, limit, largestRules(bodies))
	}

	req.Prompt = renderRulesSection(refs, bodies) + req.Prompt
	req.ruleRefs = refs
	req.ruleBytes = int(total)
	return nil
}

// renderRulesSection renders the fixed section: the header, one `### <name>` per
// rule with its body, and the end marker. bodies is keyed by name because the
// project file's name (project:<key>) is derived here rather than resolved from the
// library.
func renderRulesSection(refs []RuleRef, bodies map[string]string) string {
	var b strings.Builder
	b.WriteString(rulesPromptHeader)
	b.WriteString("\n\n")
	for _, ref := range refs {
		b.WriteString("### ")
		b.WriteString(ref.Name)
		b.WriteString("\n")
		b.WriteString(bodies[ref.Name])
		b.WriteString("\n\n")
	}
	b.WriteString(rulesPromptEnd)
	b.WriteString("\n\n")
	return b.String()
}

// ruleBodyOf strips an optional frontmatter head from a project rule file, matching
// template.SplitFrontmatter (which the rule package's BodyOf uses) byte for byte: a
// leading `---` block is metadata, everything after it is the rule text. The job
// package must not import internal/rule (G022), so the split lives on both sides.
func ruleBodyOf(raw []byte) string {
	s := strings.TrimLeft(string(raw), " \t\r\n")
	if !strings.HasPrefix(s, "---") {
		return strings.TrimSpace(string(raw))
	}
	s = s[3:]
	i := strings.Index(s, "\n---")
	if i < 0 {
		return strings.TrimSpace(string(raw))
	}
	rest := s[i+4:]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[j+1:]
	} else {
		rest = ""
	}
	return strings.TrimSpace(rest)
}

// hasRulesSection reports whether a prompt already opens with an injected rules
// section. It requires BOTH the header and the end marker, so a prompt that merely
// quotes the header (a caller discussing gofer's rules) is not mistaken for one the
// server injected.
func hasRulesSection(prompt string) bool {
	return strings.HasPrefix(prompt, rulesPromptHeader) && strings.Contains(prompt, rulesPromptEnd)
}

// stripRulesSection removes a leading injected section, returning the prompt the
// caller actually wrote. It is used by a rerun, which must re-read the current rules
// rather than replay the text a previous submit froze into request_json.
func stripRulesSection(prompt string) string {
	if !hasRulesSection(prompt) {
		return prompt
	}
	i := strings.Index(prompt, rulesPromptEnd)
	rest := prompt[i+len(rulesPromptEnd):]
	return strings.TrimLeft(rest, "\n")
}

// insertSkillsManifest places the mounted-skills list in the prompt: after the
// injected rules section when there is one, at the very top otherwise. Rules are the
// frame ("what you must obey"), the skills list is content ("what you may read"), so
// the frame stays first even though the manifest is assembled later, by the machine
// that mounted the files (skillsPromptPrefix).
func insertSkillsManifest(prompt, manifest string) string {
	if manifest == "" {
		return prompt
	}
	if !hasRulesSection(prompt) {
		return manifest + prompt
	}
	i := strings.Index(prompt, rulesPromptEnd)
	end := i + len(rulesPromptEnd)
	rest := strings.TrimLeft(prompt[end:], "\n")
	return prompt[:end] + "\n\n" + manifest + rest
}

// largestRules names the biggest rules in an over-limit set, largest first (ties
// broken by name), with their byte counts — the operator needs to know WHICH rule to
// shorten, not just that the total was too long.
func largestRules(bodies map[string]string) string {
	type entry struct {
		name string
		size int
	}
	all := make([]entry, 0, len(bodies))
	for name, body := range bodies {
		all = append(all, entry{name: name, size: len(body)})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].size != all[j].size {
			return all[i].size > all[j].size
		}
		return all[i].name < all[j].name
	})
	if len(all) > rulesLargestShown {
		all = all[:rulesLargestShown]
	}
	parts := make([]string, 0, len(all))
	for _, e := range all {
		parts = append(parts, fmt.Sprintf("%s (%d bytes)", e.name, e.size))
	}
	return strings.Join(parts, ", ")
}
