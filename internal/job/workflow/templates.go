package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/inhere/gofer/internal/config"
	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
	gotemplate "github.com/inhere/gofer/internal/template"
)

// WorkflowTemplate is a named, declarative workflow recipe. Builtins are kept as
// data here so validation and the CLI/API use the exact same source.
type WorkflowTemplate struct {
	Name string `json:"name" yaml:"name"`
	Desc string `json:"desc,omitempty" yaml:"desc,omitempty"`
	// Source is "project", "global" or "builtin" (set by the lookup, not authored).
	Source string `json:"source,omitempty" yaml:"-"`
	Spec   Spec   `json:"spec" yaml:"spec"`
}

// BuiltinWorkflowTemplates returns the first-party Z3 recipes. Agents, runner and
// project are all template variables; an empty runner means "the project default"
// (the same selection rule as an ordinary job submit).
func BuiltinWorkflowTemplates() []WorkflowTemplate {
	review := true
	runnerVar := gotemplate.Var{Desc: "runner key; empty = project default / allowed_runners rule"}
	return []WorkflowTemplate{
		{
			Name: "compare", Desc: "Run one task through multiple agents in isolated worktrees and wait for a pick",
			Spec: Spec{Title: "compare: ${vars.task}", Vars: map[string]gotemplate.Var{
				"project": {Required: true, Desc: "project key"}, "task": {Required: true, Desc: "task prompt"},
				"agent_a": {Default: "claude", Desc: "first agent key"}, "agent_b": {Default: "codex", Desc: "second agent key"},
				"runner": runnerVar,
			}, Steps: []StepSpec{{Name: "compare", ProjectKey: "${vars.project}", Agents: []string{"${vars.agent_a}", "${vars.agent_b}"}, Runner: "${vars.runner}", Prompt: "${vars.task}", Worktree: true, Join: joinPick}}},
		},
		{
			// gofer-3nxa.5: the planner follows job.PlanRules and ends its plan with a
			// gofer-todos block, which `gofer plan import --from-job` turns into a todo chain.
			Name: "plan-implement", Desc: "Plan, pause for review, then implement in a worktree",
			Spec: Spec{Title: "plan-implement: ${vars.task}", Vars: map[string]gotemplate.Var{
				"project": {Required: true, Desc: "project key"}, "task": {Required: true, Desc: "task prompt"},
				"planner":     {Default: "claude", Desc: "planning agent key"},
				"implementer": {Default: "codex", Desc: "implementing agent key"},
				"runner":      runnerVar,
			}, Steps: []StepSpec{
				{Name: "plan", ProjectKey: "${vars.project}", Agent: "${vars.planner}", Runner: "${vars.runner}", Prompt: "Plan: ${vars.task}\n\n" + job.PlannerGuidance, ReadOnly: true, Review: &review},
				{Name: "implement", ProjectKey: "${vars.project}", Agent: "${vars.implementer}", Runner: "${vars.runner}", Prompt: "Implement ${steps.plan.stdout}", Worktree: true},
			}},
		},
		{
			Name: "review-committee", Desc: "Independent reviews, then a verifier agent checks every finding against the code and summarises",
			Spec: Spec{Title: "review-committee: ${vars.task}", Vars: map[string]gotemplate.Var{
				"project": {Required: true, Desc: "project key"}, "task": {Required: true, Desc: "review target"},
				"agent_a": {Default: "claude", Desc: "first reviewer"}, "agent_b": {Default: "codex", Desc: "second reviewer"},
				"verifier": {Default: "claude", Desc: "verifier agent key"},
				"runner":   runnerVar,
			}, Steps: []StepSpec{
				{Name: "reviews", ProjectKey: "${vars.project}", Agents: []string{"${vars.agent_a}", "${vars.agent_b}"}, Runner: "${vars.runner}", Prompt: "Review ${vars.task}", ReadOnly: true},
				// The review reports reach the verifier as FILE PATHS (result_dir), never
				// interpolated into a command line.
				{Name: "summary", ProjectKey: "${vars.project}", Agent: "${vars.verifier}", Runner: "${vars.runner}", ReadOnly: true,
					Prompt: "You are the verifier of a review committee. The independent review reports are the file " + store.StdoutFile + " inside each of these result directories (one per line):\n${steps.reviews.result_dir}\n" +
						"Read every report in full. For each finding, open the real source code in the current project and confirm whether it is true; discard false positives and say why. " +
						"Output one consolidated report: confirmed findings (with file:line evidence), rejected findings (with reason), and anything the reviewers missed. Do not modify any file."},
			}},
		},
	}
}

// LookupWorkflowTemplates lists the available templates in lookup order (project
// `.gofer/workflows`, global `<config-dir>/workflows`, builtin); a name found in an
// earlier source shadows later ones. Unparseable files are skipped.
func LookupWorkflowTemplates(projectDir, globalDir string) []WorkflowTemplate {
	var out []WorkflowTemplate
	seen := map[string]bool{}
	add := func(dir, source string) {
		if dir == "" {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, ent := range entries {
			ext := strings.ToLower(filepath.Ext(ent.Name()))
			if ent.IsDir() || (ext != ".yaml" && ext != ".yml" && ext != ".json") {
				continue
			}
			name := strings.TrimSuffix(ent.Name(), filepath.Ext(ent.Name()))
			if seen[name] {
				continue
			}
			spec, err := readWorkflowTemplateFile(filepath.Join(dir, ent.Name()))
			if err != nil {
				continue
			}
			seen[name] = true
			out = append(out, WorkflowTemplate{Name: name, Desc: spec.Title, Source: source, Spec: spec})
		}
	}
	if projectDir != "" {
		add(filepath.Join(projectDir, ".gofer", "workflows"), "project")
	}
	add(globalDir, "global")
	for _, tpl := range BuiltinWorkflowTemplates() {
		if !seen[tpl.Name] {
			tpl.Source = "builtin"
			out = append(out, tpl)
		}
	}
	return out
}

func readWorkflowTemplateFile(path string) (Spec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	var spec Spec
	if strings.EqualFold(filepath.Ext(path), ".json") {
		err = json.Unmarshal(body, &spec)
	} else {
		err = yaml.Unmarshal(body, &spec)
	}
	return spec, err
}

// ResolveBuiltinWorkflowTemplate applies vars/defaults and returns an executable spec.
func ResolveBuiltinWorkflowTemplate(name string, vars map[string]string) (Spec, error) {
	for _, tpl := range BuiltinWorkflowTemplates() {
		if tpl.Name == name {
			return renderWorkflowTemplate(tpl.Spec, vars)
		}
	}
	return Spec{}, fmt.Errorf("workflow template %q not found", name)
}

// ResolveWorkflowTemplate applies the documented project, global, then builtin
// lookup order. Files are YAML workflow specs with the same vars declaration.
func ResolveWorkflowTemplate(name string, vars map[string]string, projectDir, globalDir string) (Spec, error) {
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return Spec{}, fmt.Errorf("invalid workflow template name %q", name)
	}
	for _, dir := range []string{filepath.Join(projectDir, ".gofer", "workflows"), globalDir} {
		if dir == "" {
			continue
		}
		for _, ext := range []string{".yaml", ".yml", ".json"} {
			path := filepath.Join(dir, name+ext)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			spec, err := readWorkflowTemplateFile(path)
			if err != nil {
				return Spec{}, fmt.Errorf("parse workflow template %q: %w", name, err)
			}
			return renderWorkflowTemplate(spec, vars)
		}
	}
	return ResolveBuiltinWorkflowTemplate(name, vars)
}

var unresolvedVarRe = regexp.MustCompile(`\$\{vars\.[^}]*\}`)

// renderWorkflowTemplate substitutes {{x}} / ${vars.x} in every string field that
// can carry a variable (title, project, agent(s), fan agents/runners, runner,
// prompt, cmd, cwd, worktree base, task-book template and its vars, verify, tags,
// nested sub-workflows) and then rejects any placeholder left over.
func renderWorkflowTemplate(spec Spec, supplied map[string]string) (Spec, error) {
	values := make(map[string]string, len(spec.Vars))
	for name, declaration := range spec.Vars {
		switch {
		case supplied[name] != "":
			values[name] = supplied[name]
		case declaration.Default != "":
			values[name] = declaration.Default
		case declaration.Required:
			return Spec{}, fmt.Errorf("workflow template variable %q is required", name)
		default:
			values[name] = "" // optional without default: empty (e.g. runner = project default)
		}
	}
	for name := range supplied {
		if _, ok := spec.Vars[name]; !ok {
			return Spec{}, fmt.Errorf("workflow template has no variable %q", name)
		}
	}
	// Deep-copy through JSON so rendering never mutates the (shared) builtin spec.
	raw, err := json.Marshal(spec)
	if err != nil {
		return Spec{}, err
	}
	var out Spec
	if err := json.Unmarshal(raw, &out); err != nil {
		return Spec{}, err
	}
	replace := func(in string) string {
		for name, value := range values {
			in = strings.ReplaceAll(in, "{{"+name+"}}", value)
			in = strings.ReplaceAll(in, "${vars."+name+"}", value)
		}
		return in
	}
	renderSpec(&out, replace)
	out.Vars = nil
	if err := checkRenderedSpec(out); err != nil {
		return Spec{}, err
	}
	return out, nil
}

func renderSpec(spec *Spec, replace func(string) string) {
	spec.Title = replace(spec.Title)
	for i := range spec.Steps {
		s := &spec.Steps[i]
		s.Name, s.ProjectKey, s.Agent, s.Runner = replace(s.Name), replace(s.ProjectKey), replace(s.Agent), replace(s.Runner)
		s.Prompt, s.Cwd, s.WorktreeBase, s.Template = replace(s.Prompt), replace(s.Cwd), replace(s.WorktreeBase), replace(s.Template)
		for _, list := range [][]string{s.Cmd, s.Agents, s.Verify, s.Tags} {
			for j := range list {
				list[j] = replace(list[j])
			}
		}
		for j := range s.Fan {
			s.Fan[j].Agent, s.Fan[j].Runner = replace(s.Fan[j].Agent), replace(s.Fan[j].Runner)
		}
		for k, v := range s.Vars {
			s.Vars[k] = replace(v)
		}
		if s.SubWorkflow != nil {
			renderSpec(s.SubWorkflow, replace)
		}
	}
}

// checkRenderedSpec validates what substitution produced: no leftover ${vars.x},
// and agent/runner/project keys that are plain identifiers (a variable value must
// not smuggle whitespace or shell/path syntax into a routing field).
func checkRenderedSpec(spec Spec) error {
	if m := unresolvedVarRe.FindString(spec.Title); m != "" {
		return fmt.Errorf("workflow template: undefined variable %s", m)
	}
	for i, s := range spec.Steps {
		all := append([]string{s.Name, s.Prompt, s.Cwd, s.WorktreeBase, s.Template}, s.Cmd...)
		all = append(all, s.Verify...)
		all = append(all, s.Tags...)
		for _, v := range s.Vars {
			all = append(all, v)
		}
		for _, v := range all {
			if m := unresolvedVarRe.FindString(v); m != "" {
				return fmt.Errorf("workflow template step %d: undefined variable %s", i+1, m)
			}
		}
		keys := append([]string{s.ProjectKey, s.Agent, s.Runner}, s.Agents...)
		for _, f := range s.Fan {
			keys = append(keys, f.Agent, f.Runner)
		}
		for _, k := range keys {
			if k == "" {
				continue // empty agent/runner is judged by the normal submit validation
			}
			if !routingKeyRe.MatchString(k) {
				return fmt.Errorf("workflow template step %d: invalid project/agent/runner key %q", i+1, k)
			}
		}
		if s.SubWorkflow != nil {
			if err := checkRenderedSpec(*s.SubWorkflow); err != nil {
				return err
			}
		}
	}
	return nil
}

var routingKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CheckTemplateAgents reports, for a RENDERED template spec, the first step agent
// the step's project does not allow, naming the agents the project does allow. A
// built-in template defaults to agents like claude/codex that a project may not
// open; without this the submit only said "agent X is not allowed" with no way
// forward. Steps with no project / agent, an unknown project (reported by the normal
// submit path) and projects with an empty allowlist (everything allowed) are skipped.
func CheckTemplateAgents(cfg *config.Config, spec Spec) error {
	if cfg == nil {
		return nil
	}
	for _, st := range spec.Steps {
		if st.SubWorkflow != nil {
			if err := CheckTemplateAgents(cfg, *st.SubWorkflow); err != nil {
				return err
			}
		}
		proj, ok := cfg.Projects[st.ProjectKey]
		if !ok || len(proj.AllowedAgents) == 0 {
			continue
		}
		agents := append([]string(nil), st.Agents...)
		for _, f := range st.Fan {
			agents = append(agents, f.Agent)
		}
		if st.Agent != "" {
			agents = append(agents, st.Agent)
		}
		for _, a := range agents {
			if a == "" || slices.Contains(proj.AllowedAgents, a) {
				continue
			}
			return fmt.Errorf("%w: template agent %q is not allowed in project %q; agents this project allows: %s (override the template's agent variable with --var <name>=<agent>)",
				job.ErrInvalidRequest, a, st.ProjectKey, strings.Join(proj.AllowedAgents, ", "))
		}
	}
	return nil
}
