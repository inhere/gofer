package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	gotemplate "github.com/inhere/gofer/internal/template"
)

// WorkflowTemplate is a named, declarative workflow recipe. Builtins are kept as
// data here so validation and the CLI/API use the exact same source.
type WorkflowTemplate struct {
	Name string `json:"name" yaml:"name"`
	Desc string `json:"desc,omitempty" yaml:"desc,omitempty"`
	Spec Spec   `json:"spec" yaml:"spec"`
}

// BuiltinWorkflowTemplates returns the first-party Z3 recipes.
func BuiltinWorkflowTemplates() []WorkflowTemplate {
	review := true
	return []WorkflowTemplate{
		{
			Name: "compare", Desc: "Run one task through multiple agents in isolated worktrees and wait for a pick",
			Spec: Spec{Title: "compare: ${vars.task}", Vars: map[string]gotemplate.Var{
				"project": {Required: true, Desc: "project key"}, "task": {Required: true, Desc: "task prompt"},
			}, Steps: []StepSpec{{Name: "compare", ProjectKey: "${vars.project}", Agents: []string{"claude", "codex"}, Runner: "local", Prompt: "${vars.task}", Worktree: true, Join: joinPick}}},
		},
		{
			Name: "plan-implement", Desc: "Plan, pause for review, then implement in a worktree",
			Spec: Spec{Title: "plan-implement: ${vars.task}", Vars: map[string]gotemplate.Var{
				"project": {Required: true, Desc: "project key"}, "task": {Required: true, Desc: "task prompt"},
			}, Steps: []StepSpec{
				{Name: "plan", ProjectKey: "${vars.project}", Agent: "claude", Runner: "local", Prompt: "Plan: ${vars.task}", Review: &review},
				{Name: "implement", ProjectKey: "${vars.project}", Agent: "codex", Runner: "local", Prompt: "Implement ${steps.plan.stdout}", Worktree: true},
			}},
		},
		{
			Name: "review-committee", Desc: "Run independent reviews and aggregate every report",
			Spec: Spec{Title: "review-committee: ${vars.task}", Vars: map[string]gotemplate.Var{
				"project": {Required: true, Desc: "project key"}, "task": {Required: true, Desc: "review target"},
			}, Steps: []StepSpec{
				{Name: "reviews", ProjectKey: "${vars.project}", Agents: []string{"claude", "codex"}, Runner: "local", Prompt: "Review ${vars.task}", ReadOnly: true},
				{Name: "summary", ProjectKey: "${vars.project}", Agent: "exec", Runner: "local", Cmd: []string{"sh", "-c", "printf '%s' '${steps.reviews.all.stdout}'"}},
			}},
		},
	}
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
			body, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var spec Spec
			if strings.EqualFold(ext, ".json") {
				err = json.Unmarshal(body, &spec)
			} else {
				err = yaml.Unmarshal(body, &spec)
			}
			if err != nil {
				return Spec{}, fmt.Errorf("parse workflow template %q: %w", name, err)
			}
			return renderWorkflowTemplate(spec, vars)
		}
	}
	return ResolveBuiltinWorkflowTemplate(name, vars)
}

func renderWorkflowTemplate(spec Spec, supplied map[string]string) (Spec, error) {
	values := make(map[string]string, len(spec.Vars))
	for name, declaration := range spec.Vars {
		if value := supplied[name]; value != "" {
			values[name] = value
		} else if declaration.Default != "" {
			values[name] = declaration.Default
		} else if declaration.Required {
			return Spec{}, fmt.Errorf("workflow template variable %q is required", name)
		}
	}
	replace := func(in string) string {
		for name, value := range values {
			in = strings.ReplaceAll(in, "{{"+name+"}}", value)
			in = strings.ReplaceAll(in, "${vars."+name+"}", value)
		}
		return in
	}
	spec.Title = replace(spec.Title)
	for i := range spec.Steps {
		s := &spec.Steps[i]
		s.Name, s.ProjectKey, s.Agent, s.Runner = replace(s.Name), replace(s.ProjectKey), replace(s.Agent), replace(s.Runner)
		s.Prompt, s.Cwd, s.WorktreeBase, s.Template = replace(s.Prompt), replace(s.Cwd), replace(s.WorktreeBase), replace(s.Template)
		for j := range s.Cmd {
			s.Cmd[j] = replace(s.Cmd[j])
		}
		for j := range s.Agents {
			s.Agents[j] = replace(s.Agents[j])
		}
	}
	spec.Vars = nil
	return spec, nil
}
