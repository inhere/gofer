// Package prompts holds the LONG agent-facing texts — the sections gofer appends
// to a job's prompt and the time-limit notice — as one text/template per
// language, loaded from an embed.FS and parsed at startup.
//
// Why not the INI catalog: these are multi-line markdown whose structure (order,
// blank lines, sub-bullets) IS the text, and a template keeps that readable and
// reviewable. Short one-liners stay in internal/i18n's INI catalogs.
//
// Layout: prompts/<lang>/<name>.md.tmpl. Every language must carry exactly the
// same names and reference the same variables (tested). Design
// docs/design/2026-10-27-i18n-design.md §4 / §7.
package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"

	"github.com/inhere/gofer/internal/i18n"
)

// Template names (the <name> of <name>.md.tmpl).
const (
	// NameAcceptance is the acceptance-criteria section.
	NameAcceptance = "acceptance"
	// NameContract is the delivery-contract section (scope discipline,
	// knowledge capture, memory flag, declared scope).
	NameContract = "contract"
	// NameDeadline is the job's time-limit notice.
	NameDeadline = "deadline"
	// NameAutoResume is the prompt of a continuation gofer starts itself after a
	// transient error (job: execute.go autoResume).
	NameAutoResume = "auto-resume"
	// NameUncommittedResume is the prompt of a continuation that first has to commit
	// what the previous run left uncommitted (job: uncommitted.go).
	NameUncommittedResume = "uncommitted-resume"
	// NameTodoContinue is the bridging line a plan item's prompt opens with when it
	// continues the session of the item before it (job: todo_continue.go).
	NameTodoContinue = "todo-continue"
	// NameDeliveryCheckRequest is the request a delivery-check agent answers
	// (job: delivery_check.go).
	NameDeliveryCheckRequest = "delivery-check-request"
	// NameDeliveryCheckResume is the prompt of the continuation a failed delivery
	// check starts (job: delivery_check.go).
	NameDeliveryCheckResume = "delivery-check-resume"
	// NameFollowUp is the bridge prompt for queued follow-up requests.
	NameFollowUp = "follow-up"
	// NameStewardRole is the standing role prompt for a steward session.
	NameStewardRole = "steward_role"
	// NameStewardReview is the default review instruction.
	NameStewardReview = "steward_review"
	// NameStewardToday is the review step that asks for advice on the Today queue.
	NameStewardToday = "steward_today"
	// NameStewardMemory is the review step that proposes memory cleanup.
	NameStewardMemory = "steward_memory"
	// NameStewardQuestion is the heading a question from a person is wrapped in.
	NameStewardQuestion = "steward_question"
	// NameRepoBlock is the instruction block `gofer repo init` keeps up to date in
	// a repository's AGENTS.md / CLAUDE.md (tracker: repo_block.go).
	NameRepoBlock = "repo_block"
	// NamePlanRules is the six rules an implementation plan follows (job: plan_rules.go).
	NamePlanRules = "plan-rules"
	// NamePlanTodosFormat describes the fenced todos block a plan ends with
	// (job: plan_rules.go).
	NamePlanTodosFormat = "plan-todos-format"
	// NameResidentOpening is the opening prompt of a mailbox resident session
	// (mail: resident.go).
	NameResidentOpening = "resident-opening"
)

// The templates' data. Each language of a name takes the same data type, so the
// fields are shared between languages.
type (
	// AcceptanceData feeds NameAcceptance: the criteria the submitter wrote.
	AcceptanceData struct {
		Acceptance string
	}
	// ContractData feeds NameContract. Scope toggles the scope-discipline lines,
	// Knowledge the knowledge-capture line, ScopeList the declared scope ("" =
	// none declared).
	ContractData struct {
		Scope     bool
		Knowledge bool
		ScopeList string
	}
	// DeadlineData feeds NameDeadline: the limit as a person reads it ("30 min")
	// and the environment variable that carries the deadline instant.
	DeadlineData struct {
		Limit string
		Env   string
	}
	// AutoResumeData feeds NameAutoResume: the transient error that interrupted the run.
	AutoResumeData struct {
		Hit string
	}
	// UncommittedResumeData feeds NameUncommittedResume: how many files the previous
	// run left uncommitted and which ones to look at (a cut-down list — Count is the
	// full number).
	UncommittedResumeData struct {
		Count int
		Files []string
	}
	// TodoContinueData feeds NameTodoContinue: the title of the item whose session is
	// continued.
	TodoContinueData struct {
		Title string
	}
	// DeliveryCheckRequestData feeds NameDeliveryCheckRequest: the evidence JSON.
	DeliveryCheckRequestData struct {
		Evidence string
	}
	// DeliveryCheckResumeData feeds NameDeliveryCheckResume: the check round and the
	// unmet items, each labelled with the requirement section it came from.
	DeliveryCheckResumeData struct {
		Round int
		Items []DeliveryCheckResumeItem
	}
	// DeliveryCheckResumeItem is one unmet item of a failed delivery check.
	DeliveryCheckResumeItem struct {
		Source, Requirement, Evidence, Action string
	}
	// FollowUpData feeds NameFollowUp with the numbered requests to complete.
	FollowUpData struct {
		Requests string
	}
	// StewardTodayData feeds NameStewardToday: the step number the section continues
	// from and how many Today cards still wait for advice (negative = unknown).
	StewardTodayData struct {
		Step    int
		Pending int
	}
	// StewardMemoryData feeds NameStewardMemory: the step number, the number of open
	// findings, how many suggestions today still allows, the finding lines to quote and
	// how many more there are (More = 0 shows none).
	StewardMemoryData struct {
		Step      int
		Findings  int
		Remaining int
		Lines     []string
		More      int
	}
	// StewardQuestionData feeds NameStewardQuestion: who asks and what.
	StewardQuestionData struct {
		By   string
		Text string
	}
	// RepoBlockData feeds NameRepoBlock: the commit-policy line and the memory
	// hint, both taken from the same catalog prime reads, so the block written into
	// the repository agrees with what a session-opening prime says.
	RepoBlockData struct {
		CommitPolicy string
		MemoryHint   string
	}
	// PlanTodosFormatData feeds NamePlanTodosFormat: the info string of the fence.
	PlanTodosFormatData struct {
		Fence string
	}
	// ResidentOpeningData feeds NameResidentOpening: the project the session serves
	// ("" = none), the configured agent name, the letter tag its letters arrive in and
	// the idle limit as a person reads it.
	ResidentOpeningData struct {
		Project string
		Name    string
		Tag     string
		Idle    string
	}
)

//go:embed en zh-CN
var tmplFS embed.FS

// parsed holds every template by name and language. It is built once, at
// startup: a missing or unparsable template fails the process instead of
// surprising somebody at job submit time.
var parsed = mustParse()

func mustParse() map[string]map[i18n.Lang]*template.Template {
	out := make(map[string]map[i18n.Lang]*template.Template, 0)
	err := eachTemplate(func(lang i18n.Lang, name, file string) error {
		raw, rerr := fs.ReadFile(tmplFS, file)
		if rerr != nil {
			return rerr
		}
		t, terr := template.New(name).Option("missingkey=error").Parse(string(raw))
		if terr != nil {
			return fmt.Errorf("%s: %w", file, terr)
		}
		if out[name] == nil {
			out[name] = make(map[i18n.Lang]*template.Template, 0)
		}
		out[name][lang] = t
		return nil
	})
	if err != nil {
		panic("i18n: load prompt templates: " + err.Error())
	}
	// Every name must exist in every language: a template nobody translated is
	// a prompt that would silently fall back to another language.
	for name, byLang := range out {
		for _, lang := range i18n.Supported() {
			if byLang[lang] == nil {
				panic(fmt.Sprintf("i18n: prompt template %q is missing for %s", name, lang))
			}
		}
	}
	return out
}

// eachTemplate walks the embedded tree, calling fn for every template file.
func eachTemplate(fn func(lang i18n.Lang, name, file string) error) error {
	return fs.WalkDir(tmplFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		lang, ok := i18n.Normalize(path.Base(path.Dir(p)))
		if !ok {
			return fmt.Errorf("%s: unknown language directory %q", p, path.Base(path.Dir(p)))
		}
		name := strings.TrimSuffix(path.Base(p), ".md.tmpl")
		if name == path.Base(p) {
			return fmt.Errorf("unexpected file %s (want <name>.md.tmpl)", p)
		}
		return fn(lang, name, p)
	})
}

// Names returns the template names, sorted.
func Names() []string {
	out := make([]string, 0, len(parsed))
	for name := range parsed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Render renders the template name in lang with data. The result carries no
// trailing newline, so a caller can join sections with a blank line.
func Render(lang i18n.Lang, name string, data any) (string, error) {
	t := parsed[name][i18n.Resolve(lang)]
	if t == nil {
		return "", fmt.Errorf("unknown prompt template %q", name)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render prompt %q for %s: %w", name, i18n.Resolve(lang), err)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// MustRender is Render for a caller that cannot recover (a prompt this process
// must produce): an unknown name or a bad data shape is a programming error.
func MustRender(lang i18n.Lang, name string, data any) string {
	out, err := Render(lang, name, data)
	if err != nil {
		panic("i18n: " + err.Error())
	}
	return out
}

// Fields returns the data field names the template name references in lang —
// the guard that a translated template uses the same variables as its siblings.
func Fields(lang i18n.Lang, name string) []string {
	t := parsed[name][i18n.Resolve(lang)]
	if t == nil {
		return nil
	}
	var out []string
	collectFields(t.Tree.Root, &out)
	sort.Strings(out)
	return out
}

// collectFields gathers every .Field reference under n.
func collectFields(n parse.Node, out *[]string) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			collectFields(c, out)
		}
	case *parse.ActionNode:
		collectFields(n.Pipe, out)
	case *parse.IfNode:
		collectFields(n.Pipe, out)
		collectFields(n.List, out)
		collectFields(n.ElseList, out)
	case *parse.RangeNode:
		collectFields(n.Pipe, out)
		collectFields(n.List, out)
		collectFields(n.ElseList, out)
	case *parse.WithNode:
		collectFields(n.Pipe, out)
		collectFields(n.List, out)
		collectFields(n.ElseList, out)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, c := range n.Cmds {
			collectFields(c, out)
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			collectFields(a, out)
		}
	case *parse.FieldNode:
		if len(n.Ident) > 0 {
			*out = append(*out, n.Ident[0])
		}
	}
}
