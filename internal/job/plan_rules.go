package job

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// Plan rules (gofer-3nxa.5, design 2026-10-10-handoff-brief-and-knowledge-loop §四):
// how an implementation plan is written so it can run as a todo chain. PlanRules is the
// single source — the plan-implement workflow template injects PlannerGuidance into its
// planner prompt, and the gofer-usage skill quotes the same text.

// PlanTodosFence is the info string of the fenced block a plan ends with.
const PlanTodosFence = "gofer-todos"

// PlanRulesIn are the six rules a plan follows, in lang.
func PlanRulesIn(lang i18n.Lang) string {
	return prompts.MustRender(lang, prompts.NamePlanRules, struct{}{})
}

// PlanTodosFormatIn describes the gofer-todos block (`gofer plan import` reads it),
// in lang.
func PlanTodosFormatIn(lang i18n.Lang) string {
	return prompts.MustRender(lang, prompts.NamePlanTodosFormat, prompts.PlanTodosFormatData{Fence: PlanTodosFence})
}

// PlannerGuidanceIn is what the plan-implement planner prompt carries: the rules, then
// the output format, in lang.
func PlannerGuidanceIn(lang i18n.Lang) string {
	return PlanRulesIn(lang) + "\n\n" + PlanTodosFormatIn(lang)
}

// The built-in workflow templates are static text (no job, so no project language to
// follow) and are written in Chinese throughout, so they carry the zh-CN guidance.
var (
	// PlanRules are the six rules a plan follows (zh-CN).
	PlanRules = PlanRulesIn(i18n.ZhCN)
	// PlanTodosFormat describes the gofer-todos block (zh-CN).
	PlanTodosFormat = PlanTodosFormatIn(i18n.ZhCN)
	// PlannerGuidance is the rules then the output format (zh-CN).
	PlannerGuidance = PlannerGuidanceIn(i18n.ZhCN)
)

// PlanTodoSpec is one step of a gofer-todos block. After holds the raw references
// (titles or 1-based step numbers); AfterSet distinguishes "omitted" (= the previous
// step) from an explicit empty list (= no dependency).
type PlanTodoSpec struct {
	Title       string
	After       []string
	AfterSet    bool
	Acceptance  string
	CheckPrompt string
	Scope       []string
	Check       string
}

// ImportTodo is one todo `plan import` creates, in creation order. After are indexes
// into the same slice (always earlier ones). A step's check becomes its own exec item
// (Assignee exec, Cmd the command line) right after it.
type ImportTodo struct {
	Title       string   `json:"title"`
	Assignee    string   `json:"assignee,omitempty"`
	Cmd         string   `json:"cmd,omitempty"`
	Acceptance  string   `json:"acceptance,omitempty"`
	CheckPrompt string   `json:"check_prompt,omitempty"`
	Scope       []string `json:"scope,omitempty"`
	After       []int    `json:"after,omitempty"`
	// Step is the 1-based gofer-todos step this item comes from.
	Step int `json:"step"`
}

var planTodosFenceRE = regexp.MustCompile("^\\s{0,3}(```|~~~)\\s*" + PlanTodosFence + "\\s*$")

// ExtractPlanTodosBlock returns the body of the LAST gofer-todos fenced block of text
// (a plan report may quote the format earlier), and whether one was found.
func ExtractPlanTodosBlock(text string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	body, found := "", false
	for i := 0; i < len(lines); i++ {
		m := planTodosFenceRE.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		var buf []string
		j := i + 1
		for ; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), m[1]) {
				break
			}
			buf = append(buf, lines[j])
		}
		body, found = strings.Join(buf, "\n"), true
		i = j
	}
	return body, found
}

// ParsePlanTodos parses the last gofer-todos block of text into steps. Unknown keys are
// an error (a typo would otherwise drop a field silently).
func ParsePlanTodos(text string) ([]PlanTodoSpec, error) {
	block, ok := ExtractPlanTodosBlock(text)
	if !ok {
		return nil, fmt.Errorf("no ```%s``` block found", PlanTodosFence)
	}
	var raw []map[string]any
	if err := yaml.Unmarshal([]byte(block), &raw); err != nil {
		return nil, fmt.Errorf("%s block is not a YAML list: %w", PlanTodosFence, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%s block has no steps", PlanTodosFence)
	}
	specs := make([]PlanTodoSpec, 0, len(raw))
	for i, item := range raw {
		n := i + 1
		var spec PlanTodoSpec
		for k, v := range item {
			var err error
			switch k {
			case "title":
				spec.Title, err = yamlText(v, false)
			case "after":
				spec.AfterSet = true
				spec.After, err = yamlList(v, false)
			case "acceptance":
				spec.Acceptance, err = yamlText(v, true)
			case "scope":
				spec.Scope, err = yamlList(v, true)
			case "check":
				spec.Check, err = yamlText(v, false)
			case "check_prompt":
				spec.CheckPrompt, err = yamlText(v, false)
			default:
				err = fmt.Errorf("unknown key %q (title, after, acceptance, check_prompt, scope, check)", k)
			}
			if err != nil {
				return nil, fmt.Errorf("step %d: %s: %w", n, k, err)
			}
		}
		if spec.Title = strings.TrimSpace(spec.Title); spec.Title == "" {
			return nil, fmt.Errorf("step %d: title is required", n)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// yamlText reads a scalar as text; with list allowed, a list of strings becomes a
// markdown list ("- a\n- b").
func yamlText(v any, list bool) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(t), nil
	case []any:
		if !list {
			return "", fmt.Errorf("want a single value")
		}
		items, err := yamlList(t, false)
		if err != nil {
			return "", err
		}
		for i := range items {
			items[i] = "- " + items[i]
		}
		return strings.Join(items, "\n"), nil
	case map[string]any:
		return "", fmt.Errorf("want text, got a mapping")
	default:
		return strings.TrimSpace(fmt.Sprint(t)), nil
	}
}

// yamlList reads a scalar or a list of scalars; with csv, a scalar is split on commas.
func yamlList(v any, csv bool) ([]string, error) {
	var in []any
	switch t := v.(type) {
	case nil:
		return []string{}, nil
	case []any:
		in = t
	case map[string]any:
		return nil, fmt.Errorf("want a value or a list, got a mapping")
	default:
		s := strings.TrimSpace(fmt.Sprint(t))
		if !csv {
			in = []any{s}
			break
		}
		for _, part := range strings.Split(s, ",") {
			in = append(in, part)
		}
	}
	out := make([]string, 0, len(in))
	for _, e := range in {
		switch e.(type) {
		case []any, map[string]any:
			return nil, fmt.Errorf("list items must be plain values")
		}
		if s := strings.TrimSpace(fmt.Sprint(e)); s != "" && e != nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// BuildTodoImport turns parsed steps into the todos to create, in order: each step one
// item (assigned to assign, "" = nobody yet), then — when it has a check — an exec item
// running the check after it. A reference to an earlier step (its title or 1-based
// number) resolves to that step's check item when it has one, so a checkpoint gates
// everything after it. An omitted `after` means the previous step; a reference to the
// step itself or a later one is refused (a step never uses what a later step creates).
func BuildTodoImport(specs []PlanTodoSpec, assign string) ([]ImportTodo, error) {
	return BuildTodoImportIn(i18n.Default, specs, assign)
}

// BuildTodoImportIn is BuildTodoImport with the title of a check item written in lang.
func BuildTodoImportIn(lang i18n.Lang, specs []PlanTodoSpec, assign string) ([]ImportTodo, error) {
	assign = strings.TrimSpace(assign)
	if assign == agent.ExecAgentKey {
		return nil, fmt.Errorf("--assign %s: steps are agent work; checks become exec items on their own", agent.ExecAgentKey)
	}
	byTitle := make(map[string][]int, len(specs))
	for i, s := range specs {
		byTitle[s.Title] = append(byTitle[s.Title], i)
	}
	var out []ImportTodo
	gate := make([]int, len(specs)) // the item a later step waits for, per step
	for i, s := range specs {
		n := i + 1
		var steps []int
		switch {
		case !s.AfterSet:
			if i > 0 {
				steps = []int{i - 1}
			}
		default:
			for _, ref := range s.After {
				dep, err := resolveStepRef(ref, byTitle, len(specs))
				if err != nil {
					return nil, fmt.Errorf("step %d (%s): after %q: %w", n, s.Title, ref, err)
				}
				if dep >= i {
					return nil, fmt.Errorf("step %d (%s): after %q points at itself or a later step", n, s.Title, ref)
				}
				steps = append(steps, dep)
			}
		}
		var after []int
		seen := map[int]bool{}
		for _, dep := range steps {
			if g := gate[dep]; !seen[g] {
				seen[g] = true
				after = append(after, g)
			}
		}
		out = append(out, ImportTodo{Title: s.Title, Assignee: assign, Acceptance: s.Acceptance, CheckPrompt: s.CheckPrompt, Scope: s.Scope, After: after, Step: n})
		gate[i] = len(out) - 1
		if check := strings.TrimSpace(s.Check); check != "" {
			out = append(out, ImportTodo{Title: i18n.T(lang, "job.plan.checkpoint_title", "title", s.Title), Assignee: agent.ExecAgentKey, Cmd: check, After: []int{gate[i]}, Step: n})
			gate[i] = len(out) - 1
		}
	}
	return out, nil
}

// resolveStepRef maps an `after` reference to a step index: a 1-based number, or a
// title naming exactly one step.
func resolveStepRef(ref string, byTitle map[string][]int, n int) (int, error) {
	ref = strings.TrimSpace(ref)
	if idx, ok := byTitle[ref]; ok {
		if len(idx) > 1 {
			return 0, fmt.Errorf("title is used by %d steps; use the step number", len(idx))
		}
		return idx[0], nil
	}
	if num, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		if num < 1 || num > n {
			return 0, fmt.Errorf("no step %d", num)
		}
		return num - 1, nil
	}
	return 0, fmt.Errorf("no step with that title")
}
