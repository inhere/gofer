package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// workerNodeNoConfig points the process at a stub server the way the reported node
// (bd h-aii-uzvc, container w-docker-claude) is wired: GOFER_RUN_MODE=worker, so the
// CLI is NOT in client mode, and NO config.yaml anywhere — the box runs jobs for a
// hub and owns no local server config. The connection flags carry the server address,
// exactly like the -s/--server flag bound from ${GOFER_SERVER_ADDR}.
func workerNodeNoConfig(t *testing.T, serverURL string) {
	t.Helper()
	isolateConfigEnv(t)
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	t.Setenv(job.EnvJobToken, "")
	config.InputCfgFile = ""
	jobConnOpts.server, jobConnOpts.token = serverURL, ""
	t.Cleanup(func() {
		config.InputCfgFile = ""
		jobConnOpts.server, jobConnOpts.token = "", ""
		agentSkillOpts.local, agentSkillOpts.out = false, ""
		agentListOpts.local, agentListOpts.runner = false, ""
	})
}

// skillsServer serves a stub skill list (GET /v1/skills) and returns its URL.
func skillsServer(t *testing.T, skills []map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skills" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": skills})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// skillListCmd returns the bound `agent skill list` command.
func skillListCmd(t *testing.T) *gcli.Command {
	t.Helper()
	return bindCmd(findSub(t, findSub(t, NewAgentCmd(), "skill"), "list"))
}

// TestSkillCmdFallsBackToHTTPWithoutLocalServerConfig proves the reported bug's fix:
// on a worker-mode node that has NO local server config, `agent skill ls` talks to
// the server instead of opening a local library that cannot exist there (the old
// behaviour refused with "no local gofer config found", which the operator never
// asked for by dropping --local).
func TestSkillCmdFallsBackToHTTPWithoutLocalServerConfig(t *testing.T) {
	workerNodeNoConfig(t, skillsServer(t, []map[string]any{
		{"name": "from-server", "version": "abc123", "size": 42, "description": "the server's copy"},
	}))

	out := captureOutput(t, func() {
		if err := runAgentSkillList(skillListCmd(t), nil); err != nil {
			t.Fatalf("agent skill ls on a worker node without a local config: %v", err)
		}
	})
	if !strings.Contains(out, "from-server") {
		t.Fatalf("worker mode without a local server config must read the server's library, got:\n%s", out)
	}
}

// TestSkillCmdLocalFlagForcesLocal proves --local still wins over the fallback: with
// a REACHABLE server configured, --local on a box without a local config fails on the
// local library rather than silently answering from the server.
func TestSkillCmdLocalFlagForcesLocal(t *testing.T) {
	workerNodeNoConfig(t, skillsServer(t, []map[string]any{
		{"name": "from-server", "version": "abc123", "size": 42},
	}))
	// Bind first: gcli's BoolOpt writes the flag default into the option var.
	c := skillListCmd(t)
	agentSkillOpts.local = true

	err := runAgentSkillList(c, nil)
	if err == nil {
		t.Fatal("--local without a local gofer config must fail, got a listing")
	}
	if !strings.Contains(err.Error(), "no local gofer config found") {
		t.Fatalf("--local must report the missing local config, got: %v", err)
	}
	if strings.Contains(err.Error(), "本机没有 server 配置") {
		t.Fatalf("--local must not fall back to the server, got: %v", err)
	}
}

// TestSkillCmdFallbackErrorExplainsWorkerMode pins the error a fallback prints when
// the server cannot be reached: it must name the actual decision (no local server
// config, run mode=worker) instead of sending the operator hunting for a --local flag
// they never passed.
func TestSkillCmdFallbackErrorExplainsWorkerMode(t *testing.T) {
	workerNodeNoConfig(t, "")

	err := runAgentSkillList(skillListCmd(t), nil)
	if err == nil {
		t.Fatal("worker mode without a local config and without a server address must fail")
	}
	want := "本机没有 server 配置（运行模式=worker），且连接 server 失败："
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("fallback error = %v, want it to contain %q", err, want)
	}
}

// TestAgentListSameFallback proves `agent list` follows the same rule: on a
// worker-mode node without a local server config it lists the SERVER's agents
// (capability bits included) rather than an empty local registry.
func TestAgentListSameFallback(t *testing.T) {
	workerNodeNoConfig(t, metaServer(t, map[string]any{"agents": []any{
		map[string]any{"key": "from-server", "type": "cli-agent", "batch": true, "interactive": true},
	}}))

	out := captureOutput(t, func() {
		if err := runAgentList(bindCmd(findSub(t, NewAgentCmd(), "list")), nil); err != nil {
			t.Fatalf("agent list on a worker node without a local config: %v", err)
		}
	})
	if !strings.Contains(out, "from-server") || !strings.Contains(out, "interactive=true") {
		t.Fatalf("worker mode without a local server config must list the server's agents, got:\n%s", out)
	}
}
