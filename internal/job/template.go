package job

// template.go is the SERVER side of task-book templates (SUP-01 P5, design §六):
// resolving a named template for a project, rendering it into the request's prompt,
// and merging its whitelisted job defaults. Parsing/rendering lives in
// internal/template (stdlib only, G022); the policy lives here — which directories
// are consulted, which builtins the server resolves, and what a zero-valued request
// field means.

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/template"
)

// applyTemplate folds req.Template into the request IN PLACE: it renders the
// template's body into Prompt (--prompt, when given, is APPENDED after a blank
// line), fills the request fields the caller left zero-valued with the template's
// defaults, and leaves Template/TemplateVars on the request so request_json records
// what produced the prompt (a rerun therefore replays the RENDERED text and never
// re-renders against a template that may have changed since).
//
// Precedence is 显式旗标 > 模板默认 > 项目默认: a default only fills a field the
// request left zero. Bools are the exception — false is indistinguishable from
// "unset", so a template can turn a flag ON but never force it off.
//
// A missing required variable, an unknown name or a traversal-y name is
// ErrInvalidRequest (400) BEFORE anything is persisted.
func (s *Service) applyTemplate(cfg *config.Config, req *JobRequest) error {
	name := strings.TrimSpace(req.Template)
	if name == "" {
		return nil
	}
	projectDir, globalDir := s.templateDirs(cfg, req.ProjectKey)
	tpl, err := template.Resolve(name, projectDir, globalDir)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRequest, err.Error())
	}
	rendered, err := template.Render(tpl, req.TemplateVars, template.Builtins(req.ProjectKey, req.Cwd, projectDir, s.Now()))
	if err != nil {
		return fmt.Errorf("%w: template %q: %s", ErrInvalidRequest, name, err.Error())
	}
	if len(rendered.Missing) > 0 {
		return fmt.Errorf("%w: template %q is missing required vars: %s",
			ErrInvalidRequest, name, strings.Join(rendered.Missing, ", "))
	}
	for _, w := range rendered.Warnings {
		slog.Warn("job.template_render", "event", "job.template_render", "component", "server",
			"template", name, "warning", w)
	}

	body := rendered.Prompt
	if add := strings.TrimSpace(req.Prompt); add != "" {
		if body == "" {
			body = add
		} else {
			body += "\n\n" + add
		}
	}
	req.Prompt = body

	m := tpl.Meta
	if req.Agent == "" {
		req.Agent = m.Agent
	}
	if req.TimeoutSec == 0 {
		req.TimeoutSec = m.TimeoutSec
	}
	if len(req.Tags) == 0 {
		req.Tags = m.Tags
	}
	if len(req.FallbackAgents) == 0 {
		req.FallbackAgents = m.FallbackAgents
	}
	if !req.ReadOnly {
		req.ReadOnly = m.ReadOnly
	}
	if !req.Worktree {
		req.Worktree = m.Worktree
	}
	if !req.Review {
		req.Review = m.Review
	}
	// An explicit --no-verify is an opt-OUT and wins over the template's default
	// step (validate would reject carrying both).
	if len(req.Verify) == 0 && !req.NoVerify {
		req.Verify = m.Verify
	}
	if req.VerifyTimeoutSec == 0 {
		req.VerifyTimeoutSec = m.VerifyTimeoutSec
	}
	// A template submit that names no runner anywhere still runs on the built-in
	// local runner — the default the CLI has always sent for a request without -t.
	// validate keeps requiring an explicit runner for every other request.
	if req.Runner == "" {
		req.Runner = builtinLocalRunner
	}
	return nil
}

// dropTemplate clears the template fields of a REPLAYED request (a rebuild, a
// failover takeover): the source's Prompt is already the RENDERED text, so letting
// Submit see a template again would append the task book to itself — the design's
// "rerun 不再重渲染, 照渲染后的 prompt 跑". The origin stays visible on the source job's
// own request_json and through the lineage columns.
func dropTemplate(req *JobRequest) {
	req.Template = ""
	req.TemplateVars = nil
}

// ListTemplates lists the templates a project can be driven with: the project's own
// directory first (shadowing same-named global ones), then <config-dir>/templates.
// An empty projectKey lists the global directory only; an unknown key is
// ErrUnknownProject (the HTTP layer maps it to 404).
func (s *Service) ListTemplates(projectKey string) ([]template.Info, error) {
	cfg := s.config()
	if err := checkTemplateProject(cfg, projectKey); err != nil {
		return nil, err
	}
	projectDir, globalDir := s.templateDirs(cfg, projectKey)
	return template.List(projectDir, globalDir), nil
}

// PreviewTemplate resolves ONE template for a projectKey (same lookup order as a
// submit) and renders it with vars — the read-only preview behind `template show` and
// the console's submit form. Rendering here (rather than in the client) is what makes
// the preview show the same text a submit would send: includes are expanded and
// {{head}} is the SERVER's project checkout. A missing required var is reported in
// Render.Missing, not as an error: a preview of an incomplete invocation is exactly
// what a reader is asking for.
func (s *Service) PreviewTemplate(projectKey, name string, vars map[string]string) (template.Preview, error) {
	cfg := s.config()
	if err := checkTemplateProject(cfg, projectKey); err != nil {
		return template.Preview{}, err
	}
	projectDir, globalDir := s.templateDirs(cfg, projectKey)
	tpl, err := template.Resolve(name, projectDir, globalDir)
	if err != nil {
		return template.Preview{}, err
	}
	rendered, err := template.Render(tpl, vars, template.Builtins(projectKey, "", projectDir, s.Now()))
	if err != nil {
		return template.Preview{}, fmt.Errorf("%s", err.Error())
	}
	return template.Preview{Template: tpl, Render: rendered}, nil
}

func checkTemplateProject(cfg *config.Config, projectKey string) error {
	if projectKey == "" {
		return nil
	}
	if _, ok := cfg.Projects[projectKey]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownProject, projectKey)
	}
	return nil
}

// templateDirs resolves the two template directories for a project: the project's
// ExecPath (G002 — the path the gofer PROCESS reads) and the user-level config dir's
// templates/. A project whose directory does not exist on this host (a worker-only
// project is a placeholder here) contributes no project dir: only the global
// directory is then consulted, which is exactly why the global one exists.
func (s *Service) templateDirs(cfg *config.Config, projectKey string) (projectDir, globalDir string) {
	globalDir, err := config.ConfigDir()
	if err != nil {
		globalDir = ""
	}
	if projectKey == "" {
		return "", globalDir
	}
	proj, ok := cfg.Projects[projectKey]
	if !ok {
		return "", globalDir
	}
	dir := cfg.ExecPath(proj)
	if dir == "" {
		return "", globalDir
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", globalDir
	}
	return dir, globalDir
}
