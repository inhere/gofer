package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
)

// templateFixtureYAML is the config the template write tests start from: an administrator
// (config.write), a restricted operator, and one project whose workspace is a temp dir so a
// project template write has somewhere real to land.
func templateFixtureYAML(t *testing.T, projectDir, storeRoot string) string {
	t.Helper()
	return fmt.Sprintf(`server:
  governance:
    require_admin_capability: true
  callers:
    - id: web-admin
      token: %s
      can_admin: true
    - id: web-op
      token: %s

storage:
  root: %s

projects:
  seed:
    host_path: %s
    allowed_agents:
      - exec
    allowed_runners:
      - local
    allow_exec: true
`, adminToken, opToken, filepath.ToSlash(storeRoot), filepath.ToSlash(projectDir))
}

// newTemplateTestServer builds a server over a real core with GOFER_CONFIG_DIR pointing at a
// temp dir, so the global template directory (<config-dir>/workflows) is a temp dir too. It
// returns the server and the two roots a test asserts on.
func newTemplateTestServer(t *testing.T) (s *Server, configDir, projectDir string) {
	t.Helper()
	projectDir = t.TempDir()
	storeRoot := t.TempDir()
	configDir = t.TempDir()
	t.Setenv("GOFER_CONFIG_DIR", configDir)
	srv, _, _ := newConfigWriteTestServer(t, templateFixtureYAML(t, projectDir, storeRoot), fixedDetector{})
	return srv, configDir, projectDir
}

// TestWorkflowTemplateSourceAPI asserts GET /v1/workflow-templates/{name}/source reports the
// origin, the file and the text for a project template, a global template and a builtin.
func TestWorkflowTemplateSourceAPI(t *testing.T) {
	s, configDir, projectDir := newTemplateTestServer(t)

	projectFile := filepath.Join(projectDir, ".gofer", "workflows", "op-check.yaml")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(validProjectTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "workflows", "shared.yaml"), []byte(validProjectTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	get := func(name, project string) (int, workflow.TemplateSource) {
		path := "/v1/workflow-templates/" + name + "/source"
		if project != "" {
			path += "?project=" + project
		}
		resp := do(t, s, http.MethodGet, path, adminToken, nil)
		var src workflow.TemplateSource
		if resp.StatusCode == http.StatusOK {
			decode(t, resp, &src)
		} else {
			resp.Body.Close()
		}
		return resp.StatusCode, src
	}

	code, src := get("op-check", "seed")
	if code != http.StatusOK || src.Source != "project" || src.Path != projectFile || src.Content != validProjectTemplateYAML {
		t.Fatalf("project source = %d %+v, want the project file", code, src)
	}
	if len(src.Shadows) != 0 {
		t.Fatalf("project source shadows = %v, want none", src.Shadows)
	}
	code, src = get("shared", "seed")
	if code != http.StatusOK || src.Source != "global" {
		t.Fatalf("global source = %d %+v, want global", code, src)
	}
	code, src = get("compare", "")
	if code != http.StatusOK || src.Source != "builtin" || src.Path != "" || !strings.Contains(src.Content, "steps:") {
		t.Fatalf("builtin source = %d %+v, want a builtin with YAML content", code, src)
	}

	// A project file over a builtin name is what a run reads, and the source view says which
	// sources that file hides.
	shadowing := filepath.Join(projectDir, ".gofer", "workflows", "compare.yaml")
	if err := os.WriteFile(shadowing, []byte(validProjectTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	code, src = get("compare", "seed")
	if code != http.StatusOK || src.Source != "project" || src.Path != shadowing {
		t.Fatalf("overriding source = %d %+v, want the project file", code, src)
	}
	if len(src.Shadows) != 1 || src.Shadows[0] != "builtin" {
		t.Fatalf("overriding source shadows = %v, want [builtin]", src.Shadows)
	}
	if code, _ = get("nope", "seed"); code != http.StatusNotFound {
		t.Fatalf("unknown template status = %d, want 404", code)
	}
}

const scopedToken = "scoped-token"

// validProjectTemplateYAML is a template body the save path accepts.
const validProjectTemplateYAML = `title: "verify ${vars.project}"
project: ${vars.project}
vars:
  project:
    required: true
steps:
  - name: check
    project_key: ${vars.project}
    agent: exec
    cmd: ["echo", "ok"]
    cwd: .
    timeout_sec: 60
`

// TestSaveGlobalWorkflowTemplate covers the global scope: a valid body is written under
// <config-dir>/workflows and audited; a rejected body writes nothing; a builtin name is a
// 409; a caller without config.write is a 403; delete removes it and a second delete is a
// 404.
func TestSaveGlobalWorkflowTemplate(t *testing.T) {
	s, configDir, _ := newTemplateTestServer(t)
	path := "/v1/workflow-templates/op-save?scope=global"

	resp := do(t, s, http.MethodPut, path, adminToken, map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d, want 200", resp.StatusCode)
	}
	var saved workflow.TemplateWriteResult
	decode(t, resp, &saved)
	file := filepath.Join(configDir, "workflows", "op-save.yaml")
	if saved.Path != file || saved.Uncommitted || saved.ContentLength != len(validProjectTemplateYAML) {
		t.Fatalf("save result = %+v, want %s", saved, file)
	}
	body, err := os.ReadFile(file)
	if err != nil || string(body) != validProjectTemplateYAML {
		t.Fatalf("saved file = %q, %v", body, err)
	}

	// The write is audited: who, which template, which scope, and a digest of the body.
	rows, err := s.settingsAuditStore().ListAuthAudit(jobstore.AuthAuditFilter{Subject: "op-save"})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no audit row for the template save")
	}
	row := rows[0]
	if row.Actor != "web-admin" || row.Action != "workflow.template.save" || row.Permission != "config.write" {
		t.Fatalf("audit row = %+v, want web-admin/workflow.template.save/config.write", row)
	}
	if !strings.Contains(row.Reason, "scope=global") || !strings.Contains(row.Reason, "sha256=") {
		t.Fatalf("audit reason = %q, want scope + digest", row.Reason)
	}

	// A rejected body writes nothing and keeps the old file.
	before := readFile(t, file)
	resp = do(t, s, http.MethodPut, path, adminToken, map[string]string{"content": "steps: ["})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	if after := readFile(t, file); string(after) != string(before) {
		t.Fatalf("a rejected save changed the file: %q", after)
	}

	// An invalid name is a 400.
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/bad%20name?scope=global", adminToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid name status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// A builtin name may be saved: the global file then overrides the builtin, and the
	// response says so.
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/compare?scope=global", adminToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overriding save status = %d, want 200", resp.StatusCode)
	}
	var override workflow.TemplateWriteResult
	decode(t, resp, &override)
	if !override.OverridesBuiltin || override.Path != filepath.Join(configDir, "workflows", "compare.yaml") {
		t.Fatalf("overriding save = %+v, want overrides_builtin at the global path", override)
	}
	resp = do(t, s, http.MethodGet, "/v1/workflow-templates/compare/source", adminToken, nil)
	var shadowed workflow.TemplateSource
	decode(t, resp, &shadowed)
	if shadowed.Source != "global" || len(shadowed.Shadows) != 1 || shadowed.Shadows[0] != "builtin" {
		t.Fatalf("overriding source = %+v, want global shadowing [builtin]", shadowed)
	}

	// Writing the builtin layer itself is refused: no scope, or scope=builtin.
	for _, q := range []string{"", "?scope=builtin"} {
		resp = do(t, s, http.MethodPut, "/v1/workflow-templates/compare"+q, adminToken,
			map[string]string{"content": validProjectTemplateYAML})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("builtin write %q status = %d, want 409", q, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Deleting the global override falls back to the builtin, and the name is then a 404 in
	// that layer (a builtin is not a file to delete).
	resp = do(t, s, http.MethodDelete, "/v1/workflow-templates/compare?scope=global", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("override delete status = %d, want 200", resp.StatusCode)
	}
	var deleted workflow.TemplateWriteResult
	decode(t, resp, &deleted)
	if deleted.FallsBackTo != "builtin" {
		t.Fatalf("override delete = %+v, want falls_back_to builtin", deleted)
	}
	resp = do(t, s, http.MethodDelete, "/v1/workflow-templates/compare?scope=global", adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second override delete status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Without config.write the save is refused and nothing is written.
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/op-check?scope=global", opToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("restricted save status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(configDir, "workflows", "op-check.yaml")); !os.IsNotExist(err) {
		t.Fatal("a forbidden save wrote a file")
	}

	resp = do(t, s, http.MethodDelete, path, adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}
	var gone workflow.TemplateWriteResult
	decode(t, resp, &gone)
	if gone.FallsBackTo != "" {
		t.Fatalf("delete of op-save = %+v, want no fallback (op-save is not a builtin)", gone)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("delete left the file behind")
	}
	resp = do(t, s, http.MethodDelete, path, adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestSaveProjectWorkflowTemplate asserts a project template lands in the project workspace
// as an uncommitted change and the response carries the commit command.
func TestSaveProjectWorkflowTemplate(t *testing.T) {
	s, _, projectDir := newTemplateTestServer(t)
	path := "/v1/workflow-templates/op-check?scope=project&project=seed"

	resp := do(t, s, http.MethodPut, path, adminToken, map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status = %d, want 200", resp.StatusCode)
	}
	var saved workflow.TemplateWriteResult
	decode(t, resp, &saved)
	file := filepath.Join(projectDir, ".gofer", "workflows", "op-check.yaml")
	if saved.Path != file || !saved.Uncommitted || saved.Scope != "project" {
		t.Fatalf("save result = %+v, want an uncommitted file at %s", saved, file)
	}
	if !strings.Contains(saved.CommitCommand, "git add .gofer/workflows/op-check.yaml") ||
		!strings.Contains(saved.CommitCommand, "git commit") {
		t.Fatalf("commit command = %q, want git add + git commit for the path", saved.CommitCommand)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("template file missing: %v", err)
	}

	// scope=project without a project key is a 400.
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/x?scope=project", adminToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing project status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	// An unknown project is a 404, and an unknown scope a 400.
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/x?scope=project&project=ghost", adminToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/x?scope=elsewhere", adminToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown scope status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Saving a builtin name in the project layer is allowed, and says what it hides; deleting
	// it falls back to the builtin again.
	resp = do(t, s, http.MethodPut, "/v1/workflow-templates/compare?scope=project&project=seed", adminToken,
		map[string]string{"content": validProjectTemplateYAML})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("project override status = %d, want 200", resp.StatusCode)
	}
	var projectOverride workflow.TemplateWriteResult
	decode(t, resp, &projectOverride)
	if !projectOverride.OverridesBuiltin || !projectOverride.Uncommitted {
		t.Fatalf("project override = %+v, want overrides_builtin + uncommitted", projectOverride)
	}
	resp = do(t, s, http.MethodDelete, "/v1/workflow-templates/compare?scope=project&project=seed", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("project override delete status = %d, want 200", resp.StatusCode)
	}
	var projectGone workflow.TemplateWriteResult
	decode(t, resp, &projectGone)
	if projectGone.FallsBackTo != "builtin" {
		t.Fatalf("project override delete = %+v, want falls_back_to builtin", projectGone)
	}
}

// TestWorkflowTemplateWriteRespectsCeiling asserts a caller whose ceiling covers another
// project cannot write this project's template (404, the ceiling's "does not exist for
// you") nor a global template (a global template is server-wide).
func TestWorkflowTemplateWriteRespectsCeiling(t *testing.T) {
	projectDir := t.TempDir()
	t.Setenv("GOFER_CONFIG_DIR", t.TempDir())
	s := recordingServerWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{{
			ID: "lim", Token: scopedToken, Ceiling: &config.CallerCeiling{Projects: []string{"other"}},
		}},
		Governance: config.GovernanceConfig{RequireAdminCapability: true},
	}, func(cfg *config.Config) {
		cfg.Projects["seed"] = config.ProjectConfig{
			HostPath: projectDir, AllowedAgents: []string{"exec"},
			AllowedRunners: []string{"local"}, AllowExec: true,
		}
	})
	grantAll(s) // the permission is there; only the ceiling stands in the way

	for _, path := range []string{
		"/v1/workflow-templates/x?scope=project&project=seed",
		"/v1/workflow-templates/x?scope=global",
	} {
		resp := do(t, s, http.MethodPut, path, scopedToken, map[string]string{"content": validProjectTemplateYAML})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".gofer", "workflows", "x.yaml")); !os.IsNotExist(err) {
		t.Fatal("an out-of-scope write created a project template")
	}
}

// waitWorkflowStatusToken is waitWorkflowStatus for a server built on a fixture config, whose
// callers are named instead of the default test token.
func waitWorkflowStatusToken(t *testing.T, s *Server, id, want, token string) {
	t.Helper()
	wait.Until(t, 20*time.Second, "workflow "+id+" to reach "+want, func() bool {
		resp := do(t, s, http.MethodGet, "/v1/workflows/"+id, token, nil)
		var d struct {
			Status string `json:"status"`
		}
		decode(t, resp, &d)
		return d.Status == want
	})
}

// TestSaveWorkflowAsTemplate asserts POST /v1/workflows/{id}/save-as-template serialises a
// finished workflow's exported spec into a template, and that an unknown workflow is a 404.
func TestSaveWorkflowAsTemplate(t *testing.T) {
	s, configDir, _ := newTemplateTestServer(t)

	resp := do(t, s, http.MethodPost, "/v1/workflows", adminToken, workflow.Spec{
		Steps: []workflow.StepSpec{{
			Name: "check", ProjectKey: "seed", Agent: "exec",
			Cmd: []string{"echo", "ok"}, Cwd: ".", TimeoutSec: 60,
		}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create workflow status = %d, want 200", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	waitWorkflowStatusToken(t, s, created.ID, "done", adminToken)

	resp = do(t, s, http.MethodPost, "/v1/workflows/"+created.ID+"/save-as-template", adminToken,
		map[string]string{"name": "from-run", "scope": "global"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save-as-template status = %d, want 200", resp.StatusCode)
	}
	var saved workflow.TemplateWriteResult
	decode(t, resp, &saved)
	file := filepath.Join(configDir, "workflows", "from-run.yaml")
	if saved.Path != file {
		t.Fatalf("saved path = %q, want %q", saved.Path, file)
	}
	body := readFile(t, file)
	if !strings.Contains(string(body), "check") {
		t.Fatalf("saved template does not carry the step:\n%s", body)
	}
	// The saved template is a loadable template for a run.
	if _, err := workflow.ValidateWorkflowTemplateContent(string(body)); err != nil {
		t.Fatalf("saved template does not validate: %v", err)
	}

	resp = do(t, s, http.MethodPost, "/v1/workflows/wf-nope/save-as-template", adminToken,
		map[string]string{"name": "x", "scope": "global"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown workflow status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
