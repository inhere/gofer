package job

import (
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newRoleEnvService builds a Service with a `supervisor` role preset whose Env
// presets GOFER_AGENT_ROLE=supervisor (the P3 use case): `--role supervisor`
// should inject it into the agent process env without a dedicated codex-sup agent.
// The role runs on the built-in `exec` agent so a submitted Cmd can echo the env
// var into result.json, proving the value reaches the spawned process cmd.Env.
func newRoleEnvService(t *testing.T, root string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"exec": {Type: agent.TypeExec},
		},
		Roles: map[string]config.RoleConfig{
			"supervisor": {
				Agent:   "exec",
				Project: "self",
				Env:     map[string]string{"GOFER_AGENT_ROLE": "supervisor", "SHARED": "from-role"},
			},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// TestResolveRoleMergesEnv: role.Env fills env DEFAULTS while an explicit per-job
// key wins (same precedence as the other role fields). The role's own map is not
// mutated (mergeEnv returns a fresh map).
func TestResolveRoleMergesEnv(t *testing.T) {
	cfg := &config.Config{
		Roles: map[string]config.RoleConfig{
			"supervisor": {
				Agent: "exec",
				Env:   map[string]string{"GOFER_AGENT_ROLE": "supervisor", "SHARED": "from-role"},
			},
		},
	}
	req := JobRequest{Role: "supervisor", Env: map[string]string{"SHARED": "from-job", "EXTRA": "x"}}
	if err := resolveRole(cfg, &req); err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if req.Env["GOFER_AGENT_ROLE"] != "supervisor" {
		t.Fatalf("role env default not filled: %v", req.Env)
	}
	if req.Env["SHARED"] != "from-job" {
		t.Fatalf("explicit job env did not win over role env: %v", req.Env)
	}
	if req.Env["EXTRA"] != "x" {
		t.Fatalf("explicit job env key dropped: %v", req.Env)
	}
	// The role's source map must be untouched (mergeEnv copies).
	if cfg.Roles["supervisor"].Env["SHARED"] != "from-role" {
		t.Fatalf("role.Env was mutated: %v", cfg.Roles["supervisor"].Env)
	}
}

// TestResolveRoleEnvFillsWhenJobEmpty: a job with no env gets the role env preset
// verbatim (the common `--role supervisor` path with no explicit --env).
func TestResolveRoleEnvFillsWhenJobEmpty(t *testing.T) {
	cfg := &config.Config{
		Roles: map[string]config.RoleConfig{
			"supervisor": {Agent: "exec", Env: map[string]string{"GOFER_AGENT_ROLE": "supervisor"}},
		},
	}
	req := JobRequest{Role: "supervisor"}
	if err := resolveRole(cfg, &req); err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if req.Env["GOFER_AGENT_ROLE"] != "supervisor" {
		t.Fatalf("role env not applied to envless job: %v", req.Env)
	}
}

// TestSubmitRoleEnvReachesProcess is the end-to-end proof: a `--role supervisor`
// exec job's child process sees GOFER_AGENT_ROLE=supervisor in its env (cmd.Env),
// so the gofer MCP child it spawns would inherit it and self-register as
// supervisor (P3). The exec script echoes the env var into result.json.
func TestSubmitRoleEnvReachesProcess(t *testing.T) {
	root := t.TempDir()
	s := newRoleEnvService(t, root)

	final := submitAndWait(t, s, JobRequest{
		Role: "supervisor", Runner: "local",
		Cmd: testcmd.Cmd(t, "write-role-result"), Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s): not found", final.ID)
	}
	if got.ResultJSON != `{"role":"supervisor"}` {
		t.Fatalf("GOFER_AGENT_ROLE did not reach process env, result.json=%q", got.ResultJSON)
	}
}

// TestSubmitJobEnvOverridesRoleEnv: an explicit per-job env value for the same key
// overrides the role preset at the process level (job env wins, end-to-end).
func TestSubmitJobEnvOverridesRoleEnv(t *testing.T) {
	root := t.TempDir()
	s := newRoleEnvService(t, root)

	final := submitAndWait(t, s, JobRequest{
		Role: "supervisor", Runner: "local",
		Env: map[string]string{"GOFER_AGENT_ROLE": "override"},
		Cmd: testcmd.Cmd(t, "write-role-result"), Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s): not found", final.ID)
	}
	if got.ResultJSON != `{"role":"override"}` {
		t.Fatalf("explicit job env did not win at process level, result.json=%q", got.ResultJSON)
	}
}

// TestSubmitNoRoleEnvUnaffected: a plain exec job (no role, no env) does not get a
// GOFER_AGENT_ROLE in its process env — the enhancement is purely additive.
func TestSubmitNoRoleEnvUnaffected(t *testing.T) {
	root := t.TempDir()
	s := newRoleEnvService(t, root)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "write-role-result"), Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s (err=%s)", final.Status, final.Error)
	}
	got, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("Get(%s): not found", final.ID)
	}
	if got.ResultJSON != `{"role":""}` {
		t.Fatalf("unexpected GOFER_AGENT_ROLE leaked into plain job, result.json=%q", got.ResultJSON)
	}
}
