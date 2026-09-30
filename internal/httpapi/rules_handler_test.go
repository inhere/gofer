package httpapi

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/rule"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newRulesTestServer wires a Server plus a REAL rule.Store over a temp library root,
// with two callers (an admin and a plain operator) behind governance's admin
// capability gate — the pair the write/read split is asserted against.
func newRulesTestServer(t *testing.T) (*Server, *rule.Store) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Callers: []config.CallerConfig{
				{ID: "admin", Token: "tok-admin", CanAdmin: true},
				{ID: "ops", Token: "tok-ops"},
			},
			Governance: config.GovernanceConfig{RequireAdminCapability: true},
		},
		Storage: config.StorageConfig{Root: filepath.Join(root, "store")},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, filepath.Join(root, "config.yaml"))
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st := openTestStore(t, root)
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, st, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, cfg.Server.Token, cfg.Server.AllowEmptyToken, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil)

	lib, err := rule.NewStore(filepath.Join(root, "rules"), st)
	if err != nil {
		t.Fatalf("new rule store: %v", err)
	}
	s.SetRules(lib)
	return s, lib
}

// TestRuleCRUDEndpoints drives the rule library's HTTP surface: PUT/DELETE are
// admin-only, reads are open to any authenticated caller (a job credential
// INCLUDED — a job may read the discipline it must obey), and the store's name
// grammar/404s map onto the documented statuses.
func TestRuleCRUDEndpoints(t *testing.T) {
	t.Parallel()
	s, _ := newRulesTestServer(t)

	const content = "---\ndescription: house discipline\n---\n\nNEVER push.\n"

	// A caller without can_admin may not write: the rule text every job on this
	// machine will be forced to obey is not an ordinary read.
	resp := do(t, s, http.MethodPut, "/v1/rules/house-rules", "tok-ops", map[string]any{"content": content})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT rule as a non-admin status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// The admin write creates it and answers with the indexed entry plus the text.
	resp = do(t, s, http.MethodPut, "/v1/rules/house-rules", "tok-admin", map[string]any{"content": content})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT rule status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var written ruleDetailResp
	decode(t, resp, &written)
	if written.Name != "house-rules" {
		t.Fatalf("PUT answered name=%q, want house-rules", written.Name)
	}
	if written.Description != "house discipline" {
		t.Fatalf("PUT answered description=%q, want the frontmatter's", written.Description)
	}
	if written.SHA256 == "" || written.Size != int64(len(content)) {
		t.Fatalf("PUT answered sha=%q size=%d, want the stored bytes' digest and length", written.SHA256, written.Size)
	}

	// List: index entries only, an array even when empty.
	resp = do(t, s, http.MethodGet, "/v1/rules", "tok-ops", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET rules status=%d, want 200", resp.StatusCode)
	}
	var list rulesListResp
	decode(t, resp, &list)
	if len(list.Rules) != 1 || list.Rules[0].Name != "house-rules" {
		t.Fatalf("GET rules = %+v, want the one rule", list.Rules)
	}
	if list.Rules[0].SHA256 != written.SHA256 {
		t.Fatalf("list sha = %q, want the write's %q", list.Rules[0].SHA256, written.SHA256)
	}

	// Show: the indexed entry plus the text.
	resp = do(t, s, http.MethodGet, "/v1/rules/house-rules", "tok-ops", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET rule status=%d, want 200", resp.StatusCode)
	}
	var one ruleDetailResp
	decode(t, resp, &one)
	if !strings.Contains(one.Content, "NEVER push.") {
		t.Fatalf("GET rule content = %q, want the stored text", one.Content)
	}

	// A JOB credential may READ (it must be able to see what governs it) but never
	// write it.
	jobTok := seedJobToken(t, s, "member-1", "member", "")
	resp = do(t, s, http.MethodGet, "/v1/rules", jobTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET rules as a job caller status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPut, "/v1/rules/house-rules", jobTok, map[string]any{"content": content})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT rule as a job caller status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/rules/house-rules", jobTok, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("DELETE rule as a job caller status=%d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown name, and an invalid one: 404 vs 400.
	resp = do(t, s, http.MethodGet, "/v1/rules/ghost", "tok-ops", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET unknown rule status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPut, "/v1/rules/House", "tok-admin", map[string]any{"content": content})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT with an invalid name status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodPut, "/v1/rules/house-rules", "tok-admin", map[string]any{"content": "   "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT with an empty body status=%d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE removes it (204) and a second delete is a 404, not a silent success.
	resp = do(t, s, http.MethodDelete, "/v1/rules/house-rules", "tok-admin", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE rule status=%d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/rules/house-rules", "tok-admin", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodDelete, "/v1/rules/house-rules", "tok-admin", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE of an unknown rule status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestJobCallerCannotDisableRules: a job credential may not submit `no_rules` — the
// job is exactly the thing the discipline governs (design 决策 4) — while a user
// caller may turn the rules off for one job.
func TestJobCallerCannotDisableRules(t *testing.T) {
	t.Parallel()
	s, _ := newRulesTestServer(t)
	body := job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
		NoRules: true,
	}

	jobTok := seedJobToken(t, s, "member-1", "member", "")
	resp := do(t, s, http.MethodPost, "/v1/jobs", jobTok, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("submit as a job caller with no_rules status=%d, want 403: %s", resp.StatusCode, bodyText(t, resp))
	}
	msg := bodyText(t, resp)
	if !strings.Contains(msg, "no_rules") {
		t.Fatalf("the refusal must name the field, got: %s", msg)
	}

	// The same request from a user caller is admitted (the job itself may fail for an
	// unrelated reason on a machine without `go`; the point is that the RULES gate let
	// it through).
	resp = do(t, s, http.MethodPost, "/v1/jobs", "tok-admin", body)
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("a user caller must be allowed to set no_rules: %s", bodyText(t, resp))
	}
	resp.Body.Close()
}
