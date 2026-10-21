package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
)

// Workflow template read / write (design D7, plan S5). The console and the CLI share one
// implementation: read the source a template really runs from, save global templates under
// <config-dir>/workflows, and save project templates into the project workspace as an
// uncommitted change. Builtin templates are read-only.

// templateScopes are the two writable scopes, plus the read-only builtin layer a write must
// not address directly (a global / project file OVERRIDES a builtin of the same name).
const (
	templateScopeGlobal  = "global"
	templateScopeProject = "project"
	templateScopeBuiltin = "builtin"
)

// handleWorkflowTemplateSource serves GET /v1/workflow-templates/{name}/source: the
// template's origin (project / global / builtin), the file it came from (empty for a
// builtin) and its own text, still carrying its ${vars.x} declarations.
func (s *Server) handleWorkflowTemplateSource(c *rux.Context) {
	if p := c.Query("project"); !s.requireProjectScope(c, p) {
		return
	}
	projectDir, globalDir := s.workflowTemplateDirs(c.Query("project"))
	src, err := workflow.ReadWorkflowTemplateSource(c.Param("name"), projectDir, globalDir)
	if err != nil {
		writeTemplateError(c, "workflow template not readable", err)
		return
	}
	c.JSON(http.StatusOK, src)
}

// handleSaveWorkflowTemplate serves PUT /v1/workflow-templates/{name}?scope=global
// (body {"content": "..."}) and its ?scope=project&project=<key> form. The body is
// validated before anything is written; a rejected template leaves the disk untouched.
func (s *Server) handleSaveWorkflowTemplate(c *rux.Context) {
	name := c.Param("name")
	// The write target (scope, ceiling, builtin / name check) is resolved BEFORE the body is
	// read: an out-of-scope caller gets the ceiling's 404 whatever the body says.
	dir, scope, commitCommand, ok := s.templateWriteTarget(c, name)
	if !ok {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid template body", err.Error())
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(c, http.StatusBadRequest, "invalid template body", "content is empty")
		return
	}
	path, err := workflow.SaveWorkflowTemplate(dir, name, req.Content)
	if err != nil {
		writeTemplateError(c, "workflow template rejected", err)
		return
	}
	s.auditTemplateWrite(c, "workflow.template.save", name, scope, req.Content)
	c.JSON(http.StatusOK, workflow.TemplateWriteResult{
		Name: name, Scope: scope, Path: path, ContentLength: len(req.Content),
		Uncommitted: scope == templateScopeProject, CommitCommand: commitCommand,
		OverridesBuiltin: overridesBuiltin(name),
	})
}

// handleDeleteWorkflowTemplate serves DELETE /v1/workflow-templates/{name} with the same
// ?scope= / ?project= pair: it removes THIS layer's file, so the name falls back to the next
// source (a global file, or the builtin) — the response says which. A name with no file in
// that layer is a 404.
func (s *Server) handleDeleteWorkflowTemplate(c *rux.Context) {
	name := c.Param("name")
	dir, scope, commitCommand, ok := s.templateWriteTarget(c, name)
	if !ok {
		return
	}
	path, err := workflow.DeleteWorkflowTemplate(dir, name)
	if err != nil {
		writeTemplateError(c, "workflow template not deleted", err)
		return
	}
	s.auditTemplateWrite(c, "workflow.template.delete", name, scope, "")
	c.JSON(http.StatusOK, workflow.TemplateWriteResult{
		Name: name, Scope: scope, Path: path,
		Uncommitted: scope == templateScopeProject, CommitCommand: commitCommand,
		FallsBackTo: s.fallBackTemplateSource(c, name),
	})
}

// handleSaveWorkflowAsTemplate serves POST /v1/workflows/{id}/save-as-template
// {name, scope, project?}: the workflow's exported spec (secrets already stripped by the
// export path) is serialised to YAML and saved through the same validation as an authored
// template.
func (s *Server) handleSaveWorkflowAsTemplate(c *rux.Context) {
	id := c.Param("id")
	var req struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		Project string `json:"project"`
	}
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid save-as-template request", err.Error())
		return
	}
	if req.Name == "" {
		writeError(c, http.StatusBadRequest, "invalid save-as-template request", "name is required")
		return
	}
	if wf, ok, err := s.workflow.GetWorkflow(id); err == nil && ok && !s.mayReadOwnedRow(c, wf.CallerID) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	spec, ok, _, err := s.workflow.ExportWorkflow(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "export workflow failed", err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown workflow", "no workflow with id "+id)
		return
	}
	content, err := yaml.Marshal(spec)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "serialise workflow failed", err.Error())
		return
	}
	dir, scope, commitCommand, ok := s.templateWriteTarget(c, req.Name, req.Scope, req.Project)
	if !ok {
		return
	}
	path, err := workflow.SaveWorkflowTemplate(dir, req.Name, string(content))
	if err != nil {
		writeTemplateError(c, "workflow template rejected", err)
		return
	}
	s.auditTemplateWrite(c, "workflow.template.save_as", req.Name, scope, string(content))
	c.JSON(http.StatusOK, workflow.TemplateWriteResult{
		Name: req.Name, Scope: scope, Path: path, ContentLength: len(content),
		Uncommitted: scope == templateScopeProject, CommitCommand: commitCommand,
		OverridesBuiltin: overridesBuiltin(req.Name),
	})
}

// overridesBuiltin reports whether saving `name` in a writable scope hides a builtin
// template of the same name (the supported way to replace a builtin: a project / global file
// wins over it, the builtin itself is never written).
func overridesBuiltin(name string) bool {
	_, builtin := workflow.BuiltinWorkflowTemplate(name)
	return builtin
}

// fallBackTemplateSource names the source `name` resolves to now that this layer's file is
// gone (empty when nothing defines it any more).
func (s *Server) fallBackTemplateSource(c *rux.Context, name string) string {
	projectDir, globalDir := s.workflowTemplateDirs(c.Query("project"))
	src, err := workflow.ReadWorkflowTemplateSource(name, projectDir, globalDir)
	if err != nil {
		return ""
	}
	return src.Source
}

// templateWriteTarget resolves the directory a template write goes to and the write scope.
// It answers the HTTP error itself when the request cannot be served (unknown scope, a
// project outside the caller's ceiling, an unknown project, an unwritable name). The
// scope / project values come from the query string by default; save-as-template passes
// them from its body.
func (s *Server) templateWriteTarget(c *rux.Context, name string, override ...string) (dir, scope, commitCommand string, ok bool) {
	scope, projectKey := c.Query("scope"), c.Query("project")
	if len(override) > 0 {
		scope, projectKey = override[0], ""
		if len(override) > 1 {
			projectKey = override[1]
		}
	}
	var err error
	// The ceiling comes before the scope: a project-restricted caller cannot write a template
	// at all (a global one is server-wide, a project one may be foreign), and the answer must
	// be the ceiling's 404 rather than a validation error about the request's shape.
	if !s.requireProjectScope(c, projectKey) {
		return "", "", "", false
	}
	switch scope {
	case templateScopeGlobal:
		var dir0 string
		if dir0, err = config.ConfigDir(); err != nil {
			writeError(c, http.StatusInternalServerError, "config directory unavailable", err.Error())
			return "", "", "", false
		}
		dir = filepath.Join(dir0, "workflows")
	case templateScopeProject:
		if projectKey == "" {
			writeError(c, http.StatusBadRequest, "invalid project", "scope=project needs a project key")
			return "", "", "", false
		}
		cfg := s.jobs.Config()
		var proj config.ProjectConfig
		known := false
		if cfg != nil {
			proj, known = cfg.Projects[projectKey]
		}
		if !known {
			writeError(c, http.StatusNotFound, "unknown project", projectKey)
			return "", "", "", false
		}
		// G002: gofer-side paths resolve through ExecPath (host_path or container_path per
		// server.path_view), so the file lands in the workspace the server reads.
		root := cfg.ExecPath(proj)
		if root == "" {
			writeError(c, http.StatusBadRequest, "project has no workspace", projectKey)
			return "", "", "", false
		}
		dir = filepath.Join(root, ".gofer", "workflows")
		rel := filepath.ToSlash(filepath.Join(".gofer", "workflows", name+".yaml"))
		commitCommand = "git add " + rel + " && git commit -m \"chore(gofer): update workflow template " + name + "\""
	default:
		if scope == "" || scope == templateScopeBuiltin {
			// The builtin layer is read-only, and a write that names no scope targets it. A
			// global / project file of the same name overrides the builtin instead.
			writeError(c, http.StatusConflict, "workflow template is read-only",
				"builtin template "+name+" cannot be changed; save it with scope=global or scope=project to override it")
			return "", "", "", false
		}
		writeError(c, http.StatusBadRequest, "invalid scope", `scope must be "global" or "project"`)
		return "", "", "", false
	}
	if err := workflow.CheckTemplateName(name); err != nil {
		writeTemplateError(c, "invalid template name", err)
		return "", "", "", false
	}
	return dir, scope, commitCommand, true
}

// writeTemplateError maps a template error onto its HTTP status: 404 for an unknown name,
// 409 for a read-only builtin, 400 for a rejected body / name, 500 for anything else.
func writeTemplateError(c *rux.Context, what string, err error) {
	switch {
	case errors.Is(err, workflow.ErrTemplateNotFound):
		writeError(c, http.StatusNotFound, what, err.Error())
	case errors.Is(err, workflow.ErrTemplateReadOnly):
		writeError(c, http.StatusConflict, what, err.Error())
	case workflow.IsInvalidTemplate(err):
		writeError(c, http.StatusBadRequest, what, err.Error())
	default:
		writeError(c, http.StatusInternalServerError, what, err.Error())
	}
}

// auditTemplateWrite records a template write in auth_audit: who, which template, which
// scope, and a digest of the body (length + sha256 — the YAML itself is not copied into the
// audit trail).
func (s *Server) auditTemplateWrite(c *rux.Context, action, name, scope, content string) {
	st := s.settingsAuditStore()
	if st == nil {
		return
	}
	reason := "scope=" + scope
	if content != "" {
		sum := sha256.Sum256([]byte(content))
		reason += " len=" + strconv.Itoa(len(content)) + " sha256=" + hex.EncodeToString(sum[:])
	}
	if err := st.InsertAuthAudit(jobstore.AuthAuditEntry{
		Actor:      callerFromCtx(c),
		Action:     action,
		Subject:    name,
		Permission: authz.PermConfigWrite,
		Method:     c.Req.Method,
		Path:       c.Req.URL.Path,
		Status:     http.StatusOK,
		Reason:     reason,
	}); err != nil {
		slog.Warn("httpapi: workflow template audit", "action", action, "name", name, "err", err)
	}
}
