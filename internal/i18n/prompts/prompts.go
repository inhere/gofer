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
	// NameStewardRole is the standing role prompt for a steward session.
	NameStewardRole = "steward_role"
	// NameStewardReview is the default review instruction.
	NameStewardReview = "steward_review"
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
