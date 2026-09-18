// Package template implements gofer task-book templates (SUP-01 P5, design §六):
// a markdown file with an optional YAML frontmatter, living in
// <project>/.gofer/templates/ or <config-dir>/templates/, that a submit renders
// into a job prompt.
//
// It is a data-layer package — it imports the standard library only — so the job
// service (which renders) and the CLI (which previews) can both use it without an
// import cycle (G022). Rendering is stdlib-only by design: a template is untrusted
// input from a file tree the operator owns, so a placeholder is either a declared
// variable, a builtin the caller resolved, or it stays verbatim.
package template

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// Sources of a resolved template (Template.Source / Info.Source).
const (
	SourceProject = "project"
	SourceGlobal  = "global"
)

// Directory layout: the project copy lives under the project root, the global one
// under the user config directory (config.ConfigDir()).
const (
	// DirName is the per-project template directory, relative to the project root.
	DirName = ".gofer/templates"
	// GlobalDirName is the template directory under the user config dir.
	GlobalDirName = "templates"
	// Ext is the template file extension.
	Ext = ".md"
	// maxBytes caps one template file (and one include), the same bound the md
	// submit path applies to a whole task document.
	maxBytes = 256 * 1024
	// headTimeout bounds the git call that resolves {{head}}.
	headTimeout = 5 * time.Second
)

var (
	// ErrNotFound is returned by Resolve for a valid name that no directory holds.
	ErrNotFound = errors.New("template not found")
	// ErrInvalidName is returned for a name that is not a bare file name
	// ([A-Za-z0-9_.-]+): the name reaches the filesystem, so a path separator or a
	// parent-dir token is refused instead of resolved.
	ErrInvalidName = errors.New("invalid template name")
)

// Var is one declared template variable (frontmatter `vars:`). A variable with no
// Default and Required:true must be supplied by the submitter.
type Var struct {
	Default  string `json:"default,omitempty" yaml:"default"`
	Required bool   `json:"required,omitempty" yaml:"required"`
	Desc     string `json:"desc,omitempty" yaml:"desc"`
}

// Meta is the whitelisted set of job defaults a frontmatter may preset. It maps
// 1:1 onto job.JobRequest fields; anything else in the frontmatter is rejected at
// parse time, so a task book cannot smuggle submit fields (caller, plan_id, cmd)
// past the server's own resolution.
type Meta struct {
	Agent            string   `json:"agent,omitempty" yaml:"agent"`
	Runner           string   `json:"runner,omitempty" yaml:"runner"`
	TimeoutSec       int      `json:"timeout_sec,omitempty" yaml:"timeout_sec"`
	Tags             []string `json:"tags,omitempty" yaml:"tags"`
	Verify           []string `json:"verify,omitempty" yaml:"verify"`
	VerifyTimeoutSec int      `json:"verify_timeout_sec,omitempty" yaml:"verify_timeout_sec"`
	Review           bool     `json:"review,omitempty" yaml:"review"`
	ReadOnly         bool     `json:"read_only,omitempty" yaml:"read_only"`
	Worktree         bool     `json:"worktree,omitempty" yaml:"worktree"`
	FallbackAgents   []string `json:"fallback_agents,omitempty" yaml:"fallback_agents"`
}

// Template is one parsed task book. Name/Source/Path are filled in by Resolve (and
// by ParseFile, which sets the two of them it can see); Parse itself leaves them
// empty for a source that is not a file.
type Template struct {
	Name   string         `json:"name,omitempty"`
	Source string         `json:"source,omitempty"`
	Path   string         `json:"path,omitempty"`
	Desc   string         `json:"desc,omitempty"`
	Meta   Meta           `json:"meta,omitempty"`
	Vars   map[string]Var `json:"vars,omitempty"`
	Body   string         `json:"body,omitempty"`
}

// Info is one listing entry (List). Err carries the parse error of a template that
// exists but is broken — reported, never silently hidden: that is the file a reader
// is looking for when a submit fails.
type Info struct {
	Name   string         `json:"name"`
	Source string         `json:"source"`
	Path   string         `json:"path"`
	Desc   string         `json:"desc,omitempty"`
	Meta   Meta           `json:"meta,omitempty"`
	Vars   map[string]Var `json:"vars,omitempty"`
	Err    string         `json:"error,omitempty"`
}

// Rendered is the outcome of one render: the prompt, the REQUIRED variables no
// value was supplied for (the caller rejects the submit), and the warnings a
// human should see (an undeclared placeholder kept verbatim, an unresolvable
// builtin, a nested include left unexpanded).
type Rendered struct {
	Prompt   string   `json:"prompt"`
	Missing  []string `json:"missing,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Preview is a template plus a render of it: what the read-only preview surfaces
// (CLI `template show`, the console's submit form) show a reader. Rendering happens
// on the SERVER because only the server can expand {{include: …}} and only the server
// knows the {{head}} of the project it will actually run in — a client-side preview
// would drift from the prompt the submit produces.
type Preview struct {
	Template
	Render Rendered `json:"render"`
}

// ValidName reports whether name is a bare template file name: [A-Za-z0-9_.-]+,
// with "." and ".." refused. The name is joined onto a directory, so a separator,
// a space or a parent-dir token never reaches the filesystem.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	return true
}

// ProjectDirPath / ProjectPath / GlobalDirPath / GlobalPath build the template
// locations. Exported so callers (tests, docs tooling, an operator writing a
// template) do not re-implement the layout.
func ProjectDirPath(projectDir string) string {
	return filepath.Join(projectDir, filepath.FromSlash(DirName))
}

func ProjectPath(projectDir, name string) string {
	return filepath.Join(ProjectDirPath(projectDir), name+Ext)
}

func GlobalDirPath(globalDir string) string {
	return filepath.Join(globalDir, GlobalDirName)
}

func GlobalPath(globalDir, name string) string {
	return filepath.Join(GlobalDirPath(globalDir), name+Ext)
}

// SplitFrontmatter separates a leading '---' yaml block from the body. It tolerates
// leading whitespace and \r\n line endings. ok=false when there is no opening '---'
// or no closing '---' line — the caller then treats the whole input as body. It is
// the same splitter the md submit path uses, so a task file and a template agree on
// what frontmatter is.
func SplitFrontmatter(body []byte) (fm, rest []byte, ok bool) {
	b := bytes.TrimLeft(body, " \t\r\n")
	if !bytes.HasPrefix(b, []byte("---")) {
		return nil, nil, false
	}
	b = b[3:]
	idx := bytes.Index(b, []byte("\n---"))
	if idx < 0 {
		return nil, nil, false
	}
	fm = b[:idx]
	rest = b[idx+4:] // skip the "\n---"
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:] // drop the rest of the closing '---' line
	} else {
		rest = nil
	}
	return fm, rest, true
}

// Parse parses template source: the optional frontmatter (whitelisted job defaults
// + `desc` + `vars`) and the prose body. Unknown frontmatter keys are an error.
func Parse(src []byte) (Template, error) {
	fm, body, ok := SplitFrontmatter(src)
	if !ok {
		// No frontmatter at all: the whole file is the body (an include fragment
		// like docs/examples/templates/common.md is exactly that).
		return Template{Vars: map[string]Var{}, Body: string(src)}, nil
	}
	tpl := Template{Vars: map[string]Var{}, Body: string(body)}
	var f frontmatter
	if err := yaml.UnmarshalWithOptions(fm, &f, yaml.Strict()); err != nil {
		return Template{}, fmt.Errorf("invalid frontmatter: %w", err)
	}
	tpl.Desc = f.Desc
	tpl.Meta = Meta{
		Agent:            f.Agent,
		Runner:           f.Runner,
		TimeoutSec:       f.TimeoutSec,
		Tags:             f.Tags,
		Verify:           f.Verify,
		VerifyTimeoutSec: f.VerifyTimeoutSec,
		Review:           f.Review,
		ReadOnly:         f.ReadOnly,
		Worktree:         f.Worktree,
		FallbackAgents:   f.FallbackAgents,
	}
	for name, spec := range f.Vars {
		tpl.Vars[name] = spec
	}
	return tpl, nil
}

// frontmatter is the strict wire shape of a template's header: exactly the keys the
// design whitelists (plus the description and the variable declarations).
type frontmatter struct {
	Desc             string         `yaml:"desc"`
	Agent            string         `yaml:"agent"`
	Runner           string         `yaml:"runner"`
	TimeoutSec       int            `yaml:"timeout_sec"`
	Tags             []string       `yaml:"tags"`
	Verify           []string       `yaml:"verify"`
	VerifyTimeoutSec int            `yaml:"verify_timeout_sec"`
	Review           bool           `yaml:"review"`
	ReadOnly         bool           `yaml:"read_only"`
	Worktree         bool           `yaml:"worktree"`
	FallbackAgents   []string       `yaml:"fallback_agents"`
	Vars             map[string]Var `yaml:"vars"`
}

// ParseFile parses one template file and fills the Name/Path it can derive. A read
// or parse failure carries the path, so a broken template names itself.
func ParseFile(path string) (Template, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return Template{}, err
	}
	if len(src) > maxBytes {
		return Template{}, fmt.Errorf("%s: template exceeds %d bytes", path, maxBytes)
	}
	tpl, err := Parse(src)
	if err != nil {
		return Template{}, fmt.Errorf("%s: %w", path, err)
	}
	tpl.Path = path
	tpl.Name = strings.TrimSuffix(filepath.Base(path), Ext)
	return tpl, nil
}

// Resolve finds the template named name: the project copy
// (<projectDir>/.gofer/templates/<name>.md) first, then the global one
// (<globalDir>/templates/<name>.md). An empty projectDir means the project is not
// readable on this host (a worker-only project) and only the global directory is
// consulted. A template that exists but does not parse is an error — it is never
// silently shadowed by the other copy.
func Resolve(name, projectDir, globalDir string) (Template, error) {
	if !ValidName(name) {
		return Template{}, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if projectDir != "" {
		tpl, err := ParseFile(ProjectPath(projectDir, name))
		if err == nil {
			tpl.Name, tpl.Source = name, SourceProject
			return tpl, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Template{}, err
		}
	}
	if globalDir != "" {
		tpl, err := ParseFile(GlobalPath(globalDir, name))
		if err == nil {
			tpl.Name, tpl.Source = name, SourceGlobal
			return tpl, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Template{}, err
		}
	}
	return Template{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

// List enumerates the templates both directories hold, sorted by name, with the
// project copy shadowing the global one of the same name. A directory that does not
// exist contributes nothing (a machine may have no templates at all).
func List(projectDir, globalDir string) []Info {
	out := []Info{}
	seen := map[string]bool{}
	for _, src := range []struct{ dir, source string }{
		{projectDirPath(projectDir), SourceProject},
		{GlobalDirPath(globalDir), SourceGlobal},
	} {
		if src.dir == "" {
			continue
		}
		for _, info := range listDir(src.dir, src.source) {
			if seen[info.Name] {
				continue
			}
			seen[info.Name] = true
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func projectDirPath(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	return ProjectDirPath(projectDir)
}

func listDir(dir, source string) []Info {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), Ext) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), Ext)
		if !ValidName(name) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info := Info{Name: name, Source: source, Path: path}
		tpl, err := ParseFile(path)
		if err != nil {
			info.Err = err.Error()
		} else {
			info.Desc, info.Meta, info.Vars = tpl.Desc, tpl.Meta, tpl.Vars
		}
		out = append(out, info)
	}
	return out
}

// BuiltinNames are the placeholders the SERVER resolves itself; a builtin the
// caller could not resolve ({{head}} outside a git checkout) renders empty with a
// warning instead of leaking the braces into the prompt.
var BuiltinNames = []string{"project", "cwd", "date", "head"}

// Builtins returns the standard placeholder values for one render: project / cwd /
// date always, head only when dir is inside a git checkout (so an unresolvable
// {{head}} is reported rather than rendered as a stale or invented sha).
func Builtins(projectKey, cwd, dir string, now time.Time) map[string]string {
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	out := map[string]string{
		"project": projectKey,
		"cwd":     cwd,
		"date":    now.Format("2006-01-02"),
	}
	if head := Head(dir); head != "" {
		out["head"] = head
	}
	return out
}

// Head returns the short HEAD sha of the git checkout at dir, or "" when dir is
// empty, is not a repository, or git is unavailable.
func Head(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), headTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD")
	cmd.Dir = dir
	// Read-only: never take .git/index.lock (tools-3wc).
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Render substitutes the declared variables (a supplied value beats the declared
// default; a REQUIRED variable with neither is reported in Missing, sorted),
// the builtins, and one level of same-directory {{include: file.md}}.
//
// A value the CALLER supplied is substituted even when the template did not declare
// that name — the submitter is naming a value for the very task book in front of
// them. A placeholder nobody resolved stays verbatim with a warning: an undeclared
// {{x}} is a typo, not an instruction to erase the text.
func Render(tpl Template, vars, builtins map[string]string) (Rendered, error) {
	values := make(map[string]string, len(tpl.Vars)+len(vars))
	var out Rendered
	for name, v := range vars {
		values[name] = v
	}
	for name, spec := range tpl.Vars {
		if _, ok := values[name]; ok {
			continue
		}
		if spec.Default != "" {
			values[name] = spec.Default
			continue
		}
		values[name] = ""
		if spec.Required {
			out.Missing = append(out.Missing, name)
		}
	}
	sort.Strings(out.Missing)

	dir := ""
	if tpl.Path != "" {
		dir = filepath.Dir(tpl.Path)
	}
	text, err := renderText(tpl.Body, values, builtins, dir, true, &out)
	if err != nil {
		return Rendered{}, err
	}
	out.Prompt = strings.TrimSpace(text)
	return out, nil
}

// renderText walks src once: every {{…}} token is either substituted (declared var,
// builtin, or one include) or written back verbatim with a warning. allowInclude
// is false while rendering an INCLUDED file — includes are one level deep, so a
// chain cannot turn one template into an unbounded read.
func renderText(src string, values, builtins map[string]string, dir string, allowInclude bool, out *Rendered) (string, error) {
	var b strings.Builder
	for i := 0; i < len(src); {
		open := strings.Index(src[i:], "{{")
		if open < 0 {
			b.WriteString(src[i:])
			break
		}
		start := i + open
		b.WriteString(src[i:start])
		closeAt := strings.Index(src[start:], "}}")
		if closeAt < 0 {
			// Unclosed braces are prose, not a token.
			b.WriteString(src[start:])
			break
		}
		raw := src[start : start+closeAt+2]
		token := strings.TrimSpace(src[start+2 : start+closeAt])
		i = start + closeAt + 2

		if name, isInclude := includeName(token); isInclude {
			if !allowInclude {
				out.Warnings = append(out.Warnings, fmt.Sprintf("nested include %s kept as-is (includes are one level deep)", raw))
				b.WriteString(raw)
				continue
			}
			text, err := includeText(name, dir, values, builtins, out)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
			continue
		}
		if v, ok := values[token]; ok {
			b.WriteString(v)
			continue
		}
		if v, ok := builtins[token]; ok {
			b.WriteString(v)
			continue
		}
		if isBuiltinName(token) {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s is not available for this render; rendered empty", raw))
			continue
		}
		out.Warnings = append(out.Warnings, fmt.Sprintf("undeclared %s kept as-is", raw))
		b.WriteString(raw)
	}
	return b.String(), nil
}

// includeName reports whether token is an include directive and returns the file
// name it names.
func includeName(token string) (string, bool) {
	const prefix = "include:"
	if !strings.HasPrefix(token, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(token, prefix)), true
}

// includeText reads one sibling file and renders it (without allowing further
// includes). The file name is held to the same bare-name rule as a template name.
func includeText(name, dir string, values, builtins map[string]string, out *Rendered) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("cannot include %q: the template has no directory (parsed from memory)", name)
	}
	if !ValidName(name) {
		return "", fmt.Errorf("cannot include %q: %w", name, ErrInvalidName)
	}
	src, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("include %q: %w", name, err)
	}
	if len(src) > maxBytes {
		return "", fmt.Errorf("include %q: exceeds %d bytes", name, maxBytes)
	}
	inc, err := Parse(src)
	if err != nil {
		return "", fmt.Errorf("include %q: %w", name, err)
	}
	return renderText(inc.Body, values, builtins, dir, false, out)
}

func isBuiltinName(name string) bool {
	for _, b := range BuiltinNames {
		if b == name {
			return true
		}
	}
	return false
}
