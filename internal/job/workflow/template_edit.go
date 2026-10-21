package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/goccy/go-yaml"

	job "github.com/inhere/gofer/internal/job"
)

// Template read / write support (D7): the console can show a template's own source and
// save global / project templates, so the same code serves both the source view and the
// save path, and a save always validates exactly what a run would load.

// Sentinel errors of the template read / write path. The HTTP layer maps them onto 404 /
// 409 / 400; anything else is a disk / config failure (500).
var (
	// ErrTemplateNotFound reports a template name that no project, global or builtin
	// source defines.
	ErrTemplateNotFound = errors.New("workflow template not found")
	// ErrTemplateReadOnly reports a change to a builtin template (builtins ship with the
	// binary; a project or global file must use another name).
	ErrTemplateReadOnly = errors.New("workflow template is read-only")
	// ErrInvalidTemplate wraps every rejection of a template body (parse, render, step
	// field checks), so a caller can tell "your YAML is wrong" from "the write failed".
	ErrInvalidTemplate = errors.New("invalid workflow template")
)

// TemplateNameRe is the only shape a template name may take: an authored name becomes a
// file name inside the project's `.gofer/workflows` or the global `<config-dir>/workflows`.
var TemplateNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// CheckTemplateName rejects a name that cannot be used as a template file name (a path
// separator, `..`, or an empty name).
func CheckTemplateName(name string) error {
	if name == "" || name == "." || name == ".." || !TemplateNameRe.MatchString(name) {
		return fmt.Errorf("%w: invalid template name %q (allowed characters: A-Z a-z 0-9 . _ -)", ErrInvalidTemplate, name)
	}
	return nil
}

// TemplateSource is where a template came from and its own text: the answer of
// GET /v1/workflow-templates/{name}/source. A builtin template has no Path; its Content is
// the YAML serialisation of the template spec (exactly what saving it under a new name
// would write).
type TemplateSource struct {
	Name    string `json:"name"`
	Source  string `json:"source"` // project | global | builtin
	Path    string `json:"path,omitempty"`
	Content string `json:"content"`
}

// TemplateWriteResult describes a saved or deleted template. Uncommitted / CommitCommand
// are set for a project template: its file lands in the project workspace as an
// uncommitted change, so the caller is told how to commit it (gofer never commits).
type TemplateWriteResult struct {
	Name          string `json:"name"`
	Scope         string `json:"scope"` // global | project
	Path          string `json:"path"`
	ContentLength int    `json:"content_length,omitempty"`
	Uncommitted   bool   `json:"uncommitted,omitempty"`
	CommitCommand string `json:"commit_command,omitempty"`
}

// BuiltinWorkflowTemplate returns the builtin template of that name.
func BuiltinWorkflowTemplate(name string) (WorkflowTemplate, bool) {
	for _, tpl := range BuiltinWorkflowTemplates() {
		if tpl.Name == name {
			return tpl, true
		}
	}
	return WorkflowTemplate{}, false
}

// templateLocations returns the template directories in lookup order (project, global),
// skipping the ones that do not apply. projectDir is the project workspace as the gofer
// process sees it (config.Config.ExecPath, G002).
func templateLocations(projectDir, globalDir string) []struct{ dir, source string } {
	out := make([]struct{ dir, source string }, 0, 2)
	if projectDir != "" {
		out = append(out, struct{ dir, source string }{filepath.Join(projectDir, ".gofer", "workflows"), "project"})
	}
	if globalDir != "" {
		out = append(out, struct{ dir, source string }{globalDir, "global"})
	}
	return out
}

// templateFileNames is the file name order a template name may be stored under.
func templateFileNames(name string) []string {
	return []string{name + ".yaml", name + ".yml", name + ".json"}
}

// findTemplateFile returns the file that stores `name` in dir ("" when there is none).
func findTemplateFile(dir, name string) string {
	if dir == "" {
		return ""
	}
	for _, fn := range templateFileNames(name) {
		p := filepath.Join(dir, fn)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ReadWorkflowTemplateSource returns the raw text of the named template, resolved in the
// same order a run uses: project `.gofer/workflows`, global `<config-dir>/workflows`,
// builtin.
func ReadWorkflowTemplateSource(name, projectDir, globalDir string) (TemplateSource, error) {
	if err := CheckTemplateName(name); err != nil {
		return TemplateSource{}, err
	}
	for _, loc := range templateLocations(projectDir, globalDir) {
		path := findTemplateFile(loc.dir, name)
		if path == "" {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return TemplateSource{}, fmt.Errorf("read workflow template %q: %w", name, err)
		}
		return TemplateSource{Name: name, Source: loc.source, Path: path, Content: string(body)}, nil
	}
	tpl, ok := BuiltinWorkflowTemplate(name)
	if !ok {
		return TemplateSource{}, fmt.Errorf("%w: %q", ErrTemplateNotFound, name)
	}
	body, err := yaml.Marshal(tpl.Spec)
	if err != nil {
		return TemplateSource{}, fmt.Errorf("serialise builtin workflow template %q: %w", name, err)
	}
	return TemplateSource{Name: name, Source: "builtin", Content: string(body)}, nil
}

// templatePlaceholder stands in for a variable that has no default, so a required
// variable does not make its own template unsavable. It has the shape of a routing key so
// the rendered project / agent / runner fields still pass the identifier check.
const templatePlaceholder = "template-var"

// ValidateWorkflowTemplateContent checks a template body the way a run would: the YAML
// must parse, its variables are rendered from their defaults (a variable without a default
// renders as a placeholder), the rendered spec must leave no unresolved ${vars.x} and its
// steps must pass the submit-time fan-out / failure-policy checks. Nothing is executed and
// nothing is written.
func ValidateWorkflowTemplateContent(content string) (Spec, error) {
	var spec Spec
	if err := yaml.Unmarshal([]byte(content), &spec); err != nil {
		return Spec{}, fmt.Errorf("%w: cannot parse YAML: %v", ErrInvalidTemplate, err)
	}
	if len(spec.Steps) == 0 {
		return Spec{}, fmt.Errorf("%w: the template has no steps", ErrInvalidTemplate)
	}
	supplied := make(map[string]string, len(spec.Vars))
	for name, decl := range spec.Vars {
		if decl.Default == "" {
			supplied[name] = templatePlaceholder
		}
	}
	rendered, err := renderWorkflowTemplate(spec, supplied)
	if err != nil {
		return Spec{}, wrapTemplateError(err)
	}
	if err := validateRetry(rendered); err != nil {
		return Spec{}, wrapTemplateError(err)
	}
	if err := validateFanout(rendered); err != nil {
		return Spec{}, wrapTemplateError(err)
	}
	return spec, nil
}

// wrapTemplateError marks a validation rejection as ErrInvalidTemplate while keeping the
// underlying reason (and job.ErrInvalidRequest, in case a caller checks that one).
func wrapTemplateError(err error) error {
	if errors.Is(err, ErrInvalidTemplate) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
}

// SaveWorkflowTemplate validates content and writes it as `<dir>/<name>.yaml`. Validation
// runs before anything touches the disk, so a rejected template leaves no file behind; the
// write itself goes through a temp file + rename, so a concurrent reader never sees a
// half-written template. Other extensions of the same name are removed, so the name keeps
// resolving to the file that was just saved.
func SaveWorkflowTemplate(dir, name, content string) (string, error) {
	if err := CheckTemplateName(name); err != nil {
		return "", err
	}
	if _, err := ValidateWorkflowTemplateContent(content); err != nil {
		return "", err
	}
	if dir == "" {
		return "", fmt.Errorf("no workflow template directory for scope")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create workflow template directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".template-*.tmp")
	if err != nil {
		return "", fmt.Errorf("write workflow template: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("write workflow template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("write workflow template: %w", err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("write workflow template: %w", err)
	}
	for _, other := range templateFileNames(name)[1:] {
		_ = os.Remove(filepath.Join(dir, other))
	}
	return path, nil
}

// DeleteWorkflowTemplate removes the file that defines `name` in dir. A name with no file
// there is ErrTemplateNotFound.
func DeleteWorkflowTemplate(dir, name string) (string, error) {
	if err := CheckTemplateName(name); err != nil {
		return "", err
	}
	path := findTemplateFile(dir, name)
	if path == "" {
		return "", fmt.Errorf("%w: %q", ErrTemplateNotFound, name)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("delete workflow template: %w", err)
	}
	return path, nil
}

// IsInvalidTemplate reports whether err is what the HTTP layer should answer with a 400:
// an authored template the submit-time checks rejected.
func IsInvalidTemplate(err error) bool {
	return errors.Is(err, ErrInvalidTemplate) || errors.Is(err, job.ErrInvalidRequest)
}
