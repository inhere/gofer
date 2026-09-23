package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/job"
)

const (
	adminToken = "adm-token"
	opToken    = "op-token"
)

// fixedDetector answers the template-injection gate from a literal table instead of
// the host's PATH, so which built-in agents materialize is deterministic (and a
// delete-that-falls-back can be exercised at all: the template only comes back if
// the detector says its CLI is there). A key absent from the map is unavailable.
type fixedDetector map[string]bool

func (f fixedDetector) Detect(agents map[string]config.AgentConfig) map[string]agent.DetectResult {
	out := make(map[string]agent.DetectResult, len(agents))
	for k := range agents {
		out[k] = agent.DetectResult{Available: f[k]}
	}
	return out
}

// configWriteFixture is the hand-annotated config the write tests start from. It
// carries, on purpose:
//   - a COMMENT inside the projects block (the surgical-save pin: a write to
//     `agents` must not reformat or comment-strip this block);
//   - an agent with `interactive_args: []` (the AGT-02 "interactive mode, no extra
//     argv" shape, which must survive a re-render of the agents block);
//   - a declared override of a BUILT-IN template key (`claude`), so deleting it can
//     demonstrate the fallback rather than the removal of a capability;
//   - two callers: one with can_admin, one without (the 403 gate).
func configWriteFixture(t *testing.T) (yamlText, hostDir, storeRoot string) {
	t.Helper()
	hostDir = filepath.ToSlash(t.TempDir())
	storeRoot = filepath.ToSlash(t.TempDir())
	return fmt.Sprintf(`# gofer config (hand-annotated fixture)
server:
  max_job_timeout_sec: 3600
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
  db_path: %s/gofer.db

# the seed project — keep this comment
projects:
  # hand-written, do not reformat
  seed:
    host_path: %s
    allowed_agents:
      - claude
      - mytool

agents:
  mytool:
    type: cli-agent
    command: mytool
    args:
      - run
      - "{{prompt}}"
    interactive_args: []
    session_capture: 'session_id=([A-Za-z0-9._-]+)'
  claude:
    type: cli-agent
    command: claude
    args:
      - --print
      - "{{prompt}}"
`, adminToken, opToken, storeRoot, storeRoot, hostDir), hostDir, storeRoot
}

// newConfigWriteTestServer builds a Server over a REAL core (the same wiring serve
// uses), so every write below goes through core.Update's clone→mutate→save→reload
// transaction and lands in a temp config.yaml. Returned so a test can assert on the
// FILE (the surgical-save pin) and on the event store.
func newConfigWriteTestServer(t *testing.T, yamlText string, det agent.Detector) (*Server, *core.Core, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlText), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	cr, err := core.Build(cfg, core.WithAgentDetector(det), core.WithConfigPath(cfgPath))
	if err != nil {
		t.Fatalf("build core: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })
	drainOnCleanup(t, cr.Jobs)
	s := New(&cfg.Server, "", false, cr.Jobs, cr.Workflow(), cr.Projects, cr.Agents, nil, cfg.Runners, nil, nil)
	s.SetConfigWriter(cr)
	return s, cr, cfgPath
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// topBlockText returns the top-level block for key, from its own (possibly commented)
// head line through the last non-blank line before the next top-level key. It is the
// byte-level witness the surgical-save test needs: "the projects block is untouched"
// means these exact bytes are unchanged.
func topBlockText(t *testing.T, doc, key string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, key+":") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("top-level key %q not found in:\n%s", key, doc)
	}
	// Walk back over the key's own comment head.
	for start > 0 && strings.HasPrefix(lines[start-1], "#") {
		start--
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		ln := lines[i]
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "#") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func getConfigView(t *testing.T, s *Server, token string) configView {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/config", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/config status=%d, want 200", resp.StatusCode)
	}
	var v configView
	decode(t, resp, &v)
	return v
}

func agentFromConfig(t *testing.T, v configView, key string) configAgentView {
	t.Helper()
	for _, a := range v.Agents {
		if a.Key == key {
			return a
		}
	}
	t.Fatalf("agent %q not in GET /v1/config: %+v", key, v.Agents)
	return configAgentView{}
}

func listedAgentKeys(t *testing.T, s *Server, token string) []string {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/agents", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/agents status=%d, want 200", resp.StatusCode)
	}
	var out struct {
		Agents []agentView `json:"agents"`
	}
	decode(t, resp, &out)
	keys := make([]string, 0, len(out.Agents))
	for _, a := range out.Agents {
		keys = append(keys, a.Key)
	}
	return keys
}

func contains(items []string, want string) bool {
	for _, v := range items {
		if v == want {
			return true
		}
	}
	return false
}

// runtimeAgentInteractive reads the EFFECTIVE interactive mode from the RUNNING agent
// registry (agent.Modes: interactive_args present, or the legacy flag) — the same
// resolution the job path uses, so a true here proves the write transaction's reload
// landed in the process rather than only on disk.
func runtimeAgentInteractive(t *testing.T, s *Server, key string) bool {
	t.Helper()
	ac, ok := s.agents.Get(key)
	if !ok {
		t.Fatalf("agent %q is not in the running registry", key)
	}
	_, interactive := agent.Modes(ac)
	return interactive
}

// TestEveryEditableAgentFieldIsWritable is the drift guard between the policy table
// (config.EditableAgentFields — what the console builds its form from) and the write
// path's field switch. The replace semantics CLEAR every editable field an agent body
// omits, so a table entry the switch cannot write would turn every agent edit into a
// 500 — the failure would show up only in production, on a field nobody had tried yet.
func TestEveryEditableAgentFieldIsWritable(t *testing.T) {
	fields := config.EditableAgentFields()
	if len(fields) == 0 {
		t.Fatal("no editable agent fields")
	}
	for _, name := range fields {
		var ac config.AgentConfig
		if err := applyAgentField(&ac, clearedAgentField("x", name)); err != nil {
			t.Errorf("applyAgentField(%q, null) = %v: the policy table and the write switch disagree", name, err)
		}
	}
	// And the other direction: a field the table does NOT call editable must be
	// refused rather than silently written by a forgotten case.
	if err := applyAgentField(&config.AgentConfig{}, configBodyField{
		name: "env", path: "agents.x.env", raw: json.RawMessage(`{"A":"b"}`),
	}); err == nil {
		t.Error("applyAgentField wrote `env`, which the policy table keeps read-only")
	}
}

// TestConfigSkillBindingsRoundTrip pins the JOB-10 binding lists on both sides of the
// console's form: GET /v1/config must publish `skills` (the form prefills the input it
// writes back) and the write path must accept it — for the server block and per agent.
// The two halves are one contract: an omitted editable field is CLEARED, so a view
// that did not carry `skills` was not a cosmetic gap — the console's next save erased
// the bindings (and, before the switch had a `skills` case, answered 500 instead).
func TestConfigSkillBindingsRoundTrip(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"skills": []string{"house-rules"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT server skills status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	if got := getConfigView(t, s, adminToken).Server.Skills; len(got) != 1 || got[0] != "house-rules" {
		t.Fatalf("server skills=%v, want [house-rules]", got)
	}

	agentBody := func() map[string]any {
		return map[string]any{
			"type": "cli-agent", "command": "mytool", "args": []string{"run", "{{prompt}}"},
		}
	}
	body := agentBody()
	body["skills"] = []string{"windows-apply-patch", "house-rules"}
	resp = do(t, s, http.MethodPut, "/v1/config/agents/mytool", adminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT agent skills status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	got := agentFromConfig(t, getConfigView(t, s, adminToken), "mytool")
	if len(got.Skills) != 2 || got.Skills[0] != "windows-apply-patch" || got.Skills[1] != "house-rules" {
		t.Fatalf("agent skills=%v, want [windows-apply-patch house-rules]", got.Skills)
	}
	// The levels are independent: an agents write must not touch the server's list.
	if v := getConfigView(t, s, adminToken).Server.Skills; len(v) != 1 || v[0] != "house-rules" {
		t.Fatalf("server skills=%v after an agents write, want [house-rules]", v)
	}

	// Replace semantics on the agent: a body that omits `skills` clears that agent's
	// own list (which is exactly why the console always sends the list it read).
	resp = do(t, s, http.MethodPut, "/v1/config/agents/mytool", adminToken, agentBody())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT agent without skills status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	if got = agentFromConfig(t, getConfigView(t, s, adminToken), "mytool"); len(got.Skills) != 0 {
		t.Fatalf("agent skills=%v, want them cleared", got.Skills)
	}
}

func TestAgentPutPatchesACPBlock(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	// Seed an acp-agent with an MCP child that carries an env value (through the file:
	// env is not writable by the API in either direction).
	seed := yamlText + `  acpx:
    type: acp-agent
    command: acpx
    acp:
      permission_policy: auto_allow
      load_session: true
      mcp_servers:
        - name: gofer
          command: gofer
          args: ["mcp"]
          env:
            GOFER_TOKEN: sk-child-secret
`
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	resp := do(t, s, http.MethodPost, "/v1/config/reload", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}

	// A console-shaped write: only the two members the form shows.
	resp = do(t, s, http.MethodPut, "/v1/config/agents/acpx", adminToken, map[string]any{
		"type": "acp-agent", "command": "acpx",
		"acp": map[string]any{"permission_policy": "ask"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}

	ac, ok := s.agents.Get("acpx")
	if !ok || ac.ACP == nil {
		t.Fatalf("acpx acp block=%v, want it kept", ac.ACP)
	}
	if ac.ACP.PermissionPolicy != "ask" {
		t.Fatalf("permission_policy=%q, want ask", ac.ACP.PermissionPolicy)
	}
	if ac.ACP.LoadSession == nil || !*ac.ACP.LoadSession {
		t.Fatal("load_session (a member the body did not carry) was dropped by the patch")
	}
	if len(ac.ACP.MCPServers) != 1 || ac.ACP.MCPServers[0].Env["GOFER_TOKEN"] != "sk-child-secret" {
		t.Fatalf("mcp_servers=%+v, want the child's env preserved", ac.ACP.MCPServers)
	}
	disk := string(readFile(t, cfgPath))
	if !strings.Contains(disk, "sk-child-secret") {
		t.Fatalf("the MCP env value was lost from disk:\n%s", disk)
	}
	// A member the API does not know is refused rather than dropped.
	resp = do(t, s, http.MethodPut, "/v1/config/agents/acpx", adminToken, map[string]any{
		"acp": map[string]any{"nope": 1},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown acp member status=%d, want 400", resp.StatusCode)
	}
}

// TestConfigWriteRequiresAdmin pins the can_admin gate on every WEB-04③ write route
// (design §一.1) with the SAME 403 body the project routes answer, so a console can
// treat "no permission" as one condition across the product.
func TestConfigWriteRequiresAdmin(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	before := readFile(t, cfgPath)

	cases := []struct {
		name, method, path string
		body               any
	}{
		{"agent put", http.MethodPut, "/v1/config/agents/x", map[string]any{"command": "x"}},
		{"agent delete", http.MethodDelete, "/v1/config/agents/mytool", nil},
		{"server put", http.MethodPut, "/v1/config/server", map[string]any{"max_job_timeout_sec": 10}},
		{"validate", http.MethodPost, "/v1/config/validate", map[string]any{
			"section": "agents", "key": "x", "value": map[string]any{"command": "x"},
		}},
		{"reload", http.MethodPost, "/v1/config/reload", nil},
	}
	for _, tc := range cases {
		resp := do(t, s, tc.method, tc.path, opToken, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status=%d, want 403", tc.name, resp.StatusCode)
		}
		var body errorBody
		decode(t, resp, &body)
		if body.Error != "admin not permitted for this caller" || body.Detail != "caller lacks can_admin capability" {
			t.Fatalf("%s: body=%+v, want the project routes' 403 wording", tc.name, body)
		}
	}
	if !bytes.Equal(before, readFile(t, cfgPath)) {
		t.Fatal("a forbidden write changed the config file")
	}
}

// TestAgentPutCreatesAndReloads is the endpoint's whole point: a new agent declared
// from the console is written to the file AND live in the running process (the
// reload inside the write transaction), so no RDP + hand edit is needed.
func TestAgentPutCreatesAndReloads(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	resp := do(t, s, http.MethodPut, "/v1/config/agents/jcode", adminToken, map[string]any{
		"type":             "cli-agent",
		"command":          "jcode",
		"args":             []string{"run", "{{prompt}}"},
		"interactive_args": []string{},
		"session_capture":  `(?i)^\s*jcode --resume (\S+)$`,
		"max_concurrent":   2,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT agent status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var written configWriteResp
	decode(t, resp, &written)
	if written.Status != "ok" || written.Key != "jcode" || !written.Reloaded || !written.Created {
		t.Fatalf("write resp=%+v", written)
	}
	for _, f := range []string{"args", "command", "interactive_args", "max_concurrent", "session_capture", "type"} {
		if !contains(written.Fields, f) {
			t.Fatalf("fields=%v, want %q", written.Fields, f)
		}
	}

	got := agentFromConfig(t, getConfigView(t, s, adminToken), "jcode")
	if got.Command != "jcode" || got.Type != "cli-agent" || got.MaxConcurrent != 2 {
		t.Fatalf("agent view=%+v", got)
	}
	// `interactive_args: []` (present but empty) is the AGT-02 shape for "interactive
	// mode, no extra argv" — the view must NOT flatten it to null (that would read as
	// batch-only), and the runtime must see an interactive mode.
	if got.InteractiveArgs == nil || len(got.InteractiveArgs) != 0 {
		t.Fatalf("interactive_args=%v, want a non-nil empty list", got.InteractiveArgs)
	}
	if !runtimeAgentInteractive(t, s, "jcode") {
		t.Fatal("the running server does not see jcode as interactive: the reload did not apply the definition")
	}

	disk := string(readFile(t, cfgPath))
	if !strings.Contains(disk, "jcode:") || !strings.Contains(disk, "command: jcode") {
		t.Fatalf("agent not persisted:\n%s", disk)
	}

	// Reloaded, not merely written: the agent registry the job path reads lists it.
	if keys := listedAgentKeys(t, s, adminToken); !contains(keys, "jcode") {
		t.Fatalf("GET /v1/agents=%v, want jcode (the write transaction must reload)", keys)
	}
}

// TestAgentPutUpdatesExisting pins the PUT semantics: a whole-definition replace of
// the editable field set. A field the body does not carry is CLEARED — an edit form
// must therefore always send the complete set (see the console's buildAgentWrite).
func TestAgentPutUpdatesExisting(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	before := agentFromConfig(t, getConfigView(t, s, adminToken), "mytool")
	if before.SessionCapture == "" {
		t.Fatal("fixture lost session_capture")
	}

	resp := do(t, s, http.MethodPut, "/v1/config/agents/mytool", adminToken, map[string]any{
		"type":    "cli-agent",
		"command": "mytool2",
		"args":    []string{"run", "{{prompt}}"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}

	got := agentFromConfig(t, getConfigView(t, s, adminToken), "mytool")
	if got.Command != "mytool2" {
		t.Fatalf("command=%q, want mytool2", got.Command)
	}
	if got.SessionCapture != "" {
		t.Fatalf("session_capture=%q, want cleared: an omitted editable field is replaced, not merged", got.SessionCapture)
	}
	if got.InteractiveArgs != nil {
		t.Fatalf("interactive_args=%v, want null: the fixture's [] was not in the body, so the agent is batch-only now", got.InteractiveArgs)
	}
	if disk := string(readFile(t, cfgPath)); strings.Contains(disk, "session_id=([A-Za-z0-9._-]+)") {
		t.Fatalf("the cleared field is still on disk:\n%s", disk)
	}
}

// TestAgentPutKeepsFieldsOutsideTheEditableSet is the deliberate half of the replace
// semantics: fields the write API CANNOT express (design §一.2 — `env` may hold
// plaintext secrets, `detect`/`mcp_server_name` are operator plumbing) are preserved
// verbatim. Clearing them would be the very "the form wiped what it never knew
// about" bug bd h-aii-3scy fixed for projects — and a console could never put the
// value back.
func TestAgentPutKeepsFieldsOutsideTheEditableSet(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	// Seed the non-editable fields through the file (they are not writable by API).
	seed := strings.Replace(yamlText,
		"    command: mytool\n",
		"    command: mytool\n    allow_raw_cmd: true\n    mcp_server_name: gopher\n    env:\n      MYTOOL_HOME: /opt/mytool\n",
		1)
	if seed == yamlText {
		t.Fatal("fixture edit failed")
	}
	if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	// Reload so the running process sees the reseeded file.
	resp := do(t, s, http.MethodPost, "/v1/config/reload", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}

	resp = do(t, s, http.MethodPut, "/v1/config/agents/mytool", adminToken, map[string]any{
		"type": "cli-agent", "command": "mytool2",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}

	got := agentFromConfig(t, getConfigView(t, s, adminToken), "mytool")
	if got.Command != "mytool2" {
		t.Fatalf("command=%q, want mytool2", got.Command)
	}
	if !contains(got.EnvKeys, "MYTOOL_HOME") || got.McpServerName != "gopher" || !got.AllowRawCmd {
		t.Fatalf("non-editable fields were dropped: %+v", got)
	}
	disk := string(readFile(t, cfgPath))
	if !strings.Contains(disk, "MYTOOL_HOME: /opt/mytool") {
		t.Fatalf("env lost from disk:\n%s", disk)
	}
}

// TestAgentDeleteFallsBackToBuiltin pins the delete contract (design §一.2): deleting
// an agent that overrides a BUILT-IN template is not "removing a capability" — the
// template definition comes back — and the caller is told so explicitly. A purely
// custom key simply disappears.
func TestAgentDeleteFallsBackToBuiltin(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, fixedDetector{"claude": true})

	resp := do(t, s, http.MethodDelete, "/v1/config/agents/claude", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE claude status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var fallback configAgentDeleteResp
	decode(t, resp, &fallback)
	if !fallback.FellBackToBuiltin || !fallback.Reloaded || fallback.Key != "claude" {
		t.Fatalf("delete resp=%+v, want fell_back_to_builtin=true", fallback)
	}
	if keys := listedAgentKeys(t, s, adminToken); !contains(keys, "claude") {
		t.Fatalf("GET /v1/agents=%v: the built-in claude definition must come back", keys)
	}
	if !strings.Contains(string(readFile(t, cfgPath)), "mytool:") {
		t.Fatal("the delete also dropped an unrelated agent")
	}

	resp = do(t, s, http.MethodDelete, "/v1/config/agents/mytool", adminToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE mytool status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	decode(t, resp, &fallback)
	if fallback.FellBackToBuiltin {
		t.Fatal("a custom agent has no built-in definition to fall back to")
	}
	if keys := listedAgentKeys(t, s, adminToken); contains(keys, "mytool") {
		t.Fatalf("GET /v1/agents=%v, want mytool gone", keys)
	}

	resp = do(t, s, http.MethodDelete, "/v1/config/agents/mytool", adminToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE status=%d, want 404", resp.StatusCode)
	}
}

// TestAgentPutRejectsSecretLiteral pins the secret rule (design decision 2): a
// LITERAL secret is never accepted through the console — the request is refused by
// name and pointed at the env-var reference instead. A `*_env` NAME carries no value
// and so never trips that check.
func TestAgentPutRejectsSecretLiteral(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	before := readFile(t, cfgPath)

	resp := do(t, s, http.MethodPut, "/v1/config/agents/bad", adminToken, map[string]any{
		"command": "bad",
		"token":   "sk-live-12345",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("secret literal status=%d, want 400: %s", resp.StatusCode, bodyText(t, resp))
	}
	var body errorBody
	decode(t, resp, &body)
	if !strings.Contains(body.Detail, "agents.bad.token") {
		t.Fatalf("detail=%q, want the offending field named", body.Detail)
	}
	if !strings.Contains(body.Detail, "_env") {
		t.Fatalf("detail=%q, want it to point at the env-var name as the alternative", body.Detail)
	}

	resp = do(t, s, http.MethodPut, "/v1/config/agents/ref", adminToken, map[string]any{
		"command":   "ref",
		"token_env": "GOFER_AGENT_TOKEN",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("env reference status=%d, want 400 (token_env is not an agent field): %s", resp.StatusCode, bodyText(t, resp))
	}
	decode(t, resp, &body)
	if strings.Contains(body.Detail, "secret") {
		t.Fatalf("detail=%q: an env NAME is a reference, never a secret literal", body.Detail)
	}
	if !strings.Contains(body.Detail, "token_env") {
		t.Fatalf("detail=%q, want the field named", body.Detail)
	}
	if !bytes.Equal(before, readFile(t, cfgPath)) {
		t.Fatal("a rejected write changed the config file")
	}
}

// TestServerPutRejectsRestartOnlyField pins the read-only half of the server
// whitelist (design §一.3): a listen address cannot be changed live, the request says
// exactly which field it refused, and nothing is written — a half-applied server
// block would make the running process disagree with the file.
func TestServerPutRejectsRestartOnlyField(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	before := readFile(t, cfgPath)

	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"addr":                "127.0.0.1:19999",
		"max_job_timeout_sec": 42,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", resp.StatusCode, bodyText(t, resp))
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Detail != "field not editable: server.addr" {
		t.Fatalf("detail=%q, want %q", body.Detail, "field not editable: server.addr")
	}
	if !bytes.Equal(before, readFile(t, cfgPath)) {
		t.Fatal("a partially rejected server write changed the file")
	}
	// The editable field in the same body must NOT have been applied either.
	if v := getConfigView(t, s, adminToken); v.Server.MaxJobTimeoutSec != 3600 {
		t.Fatalf("max_job_timeout_sec=%d, want the fixture's 3600 (all-or-nothing)", v.Server.MaxJobTimeoutSec)
	}
}

// TestServerPutAppliesEditableField is the server half of the goal: the everyday
// knobs are editable from the console and take effect immediately.
func TestServerPutAppliesEditableField(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"max_job_timeout_sec": 120,
		"stall_timeout_sec":   600,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var written configWriteResp
	decode(t, resp, &written)
	if written.Section != "server" || !written.Reloaded {
		t.Fatalf("resp=%+v", written)
	}
	if !contains(written.RestartRequired, "server.addr") {
		t.Fatalf("restart_required=%v, want the restart-only surface named (server.addr)", written.RestartRequired)
	}
	if contains(written.RestartRequired, "server.max_job_timeout_sec") {
		t.Fatal("a hot-editable field must not be reported as needing a restart")
	}

	v := getConfigView(t, s, adminToken)
	if v.Server.MaxJobTimeoutSec != 120 {
		t.Fatalf("max_job_timeout_sec=%d, want 120", v.Server.MaxJobTimeoutSec)
	}
	if v.Server.StallTimeoutSec == nil || *v.Server.StallTimeoutSec != 600 {
		t.Fatalf("stall_timeout_sec=%v, want 600", v.Server.StallTimeoutSec)
	}
	disk := string(readFile(t, cfgPath))
	if !strings.Contains(disk, "max_job_timeout_sec: 120") {
		t.Fatalf("not persisted:\n%s", disk)
	}

	// An explicit null clears a pointer field back to "unset" (inherit).
	resp = do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{"stall_timeout_sec": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	if v = getConfigView(t, s, adminToken); v.Server.StallTimeoutSec != nil {
		t.Fatalf("stall_timeout_sec=%v, want unset", v.Server.StallTimeoutSec)
	}
}

// TestConfigValidateDryRunDoesNotWrite pins the dry run: the same request shapes as
// the write endpoints, the full validation, the impact list — and, above all, NO
// write and NO reload (the file is compared byte for byte).
func TestConfigValidateDryRunDoesNotWrite(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	before := readFile(t, cfgPath)

	// (1) An illegal candidate: type exec cannot carry interactive_args. The answer
	// must name the field so the console can highlight that input.
	resp := do(t, s, http.MethodPost, "/v1/config/validate", adminToken, map[string]any{
		"section": "agents",
		"key":     "bad",
		"value":   map[string]any{"type": "exec", "interactive_args": []string{}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid candidate status=%d, want 400: %s", resp.StatusCode, bodyText(t, resp))
	}
	var invalid configValidateResult
	decode(t, resp, &invalid)
	if invalid.OK || len(invalid.Errors) == 0 {
		t.Fatalf("result=%+v, want ok=false with errors", invalid)
	}
	if !contains(invalid.ErrorFields, "agents.bad.interactive_args") {
		t.Fatalf("error_fields=%v, want agents.bad.interactive_args", invalid.ErrorFields)
	}

	// (2) A legal candidate: ok, and the impact list is empty for an agent (agents
	// hot-reload) — the preview is the candidate block, not a locally assembled one.
	resp = do(t, s, http.MethodPost, "/v1/config/validate", adminToken, map[string]any{
		"section": "agents",
		"key":     "ok",
		"value":   map[string]any{"type": "cli-agent", "command": "ok", "args": []string{"{{prompt}}"}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid candidate status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var valid configValidateResult
	decode(t, resp, &valid)
	if !valid.OK || len(valid.Errors) != 0 {
		t.Fatalf("result=%+v, want ok with no errors", valid)
	}
	if len(valid.RestartRequired) != 0 {
		t.Fatalf("restart_required=%v, want none for an agent edit", valid.RestartRequired)
	}
	if !strings.Contains(valid.Preview, "command: ok") {
		t.Fatalf("preview=%q, want the candidate's own YAML", valid.Preview)
	}
	if !contains(valid.Applied, "command") {
		t.Fatalf("applied=%v, want command", valid.Applied)
	}

	// (3) The server section reports its restart-only surface, so the console can say
	// "保存后立即生效" vs "以下字段仍需改文件 + 重启".
	resp = do(t, s, http.MethodPost, "/v1/config/validate", adminToken, map[string]any{
		"section": "server",
		"value":   map[string]any{"max_job_timeout_sec": 90},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("server candidate status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	decode(t, resp, &valid)
	if !contains(valid.RestartRequired, "server.addr") || !contains(valid.RestartRequired, "server.token_env") {
		t.Fatalf("restart_required=%v, want the restart-only server fields", valid.RestartRequired)
	}
	if contains(valid.RestartRequired, "server.max_job_timeout_sec") {
		t.Fatal("a hot-editable field must not be reported as needing a restart")
	}

	if !bytes.Equal(before, readFile(t, cfgPath)) {
		t.Fatal("a dry run wrote the config file")
	}
	if v := getConfigView(t, s, adminToken); v.Server.MaxJobTimeoutSec != 3600 {
		t.Fatal("a dry run changed the live config")
	}
}

// TestSurgicalSaveKeepsOtherBlockComments pins the write path itself: it must go
// through config.Save's surgical writer. If someone ever replaced this endpoint's
// save with a whole-file re-render, the hand-annotated projects block (and the
// AGT-02 `interactive_args: []` shape in the agents block) would be comment-stripped
// by a console edit that never touched them.
func TestSurgicalSaveKeepsOtherBlockComments(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, _, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	projectsBefore := topBlockText(t, string(readFile(t, cfgPath)), "projects")

	resp := do(t, s, http.MethodPut, "/v1/config/agents/jcode", adminToken, map[string]any{
		"type": "cli-agent", "command": "jcode", "args": []string{"run", "{{prompt}}"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}

	after := string(readFile(t, cfgPath))
	if got := topBlockText(t, after, "projects"); got != projectsBefore {
		t.Fatalf("projects block rewritten by an agents edit:\n--- got ---\n%s\n--- want ---\n%s", got, projectsBefore)
	}
	if !strings.Contains(after, "# hand-written, do not reformat") {
		t.Fatalf("the projects block's comment is gone:\n%s", after)
	}
	if !strings.Contains(after, "interactive_args: []") {
		t.Fatalf("the AGT-02 empty interactive_args was lost when the agents block was re-rendered:\n%s", after)
	}
	if !strings.Contains(after, "jcode:") {
		t.Fatalf("the edit itself is missing:\n%s", after)
	}
}

// TestConfigUpdatedEventRecorded pins the audit trail (design §一.5): one event per
// successful write on the `config` scope, carrying WHAT changed and WHO did it —
// and never a value (config values are paths/host names and may be sensitive).
func TestConfigUpdatedEventRecorded(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, cr, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	resp := do(t, s, http.MethodPut, "/v1/config/agents/telemetry", adminToken, map[string]any{
		"type": "cli-agent", "command": "mysecretcmd", "args": []string{"go"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}

	events, err := cr.Store.ListJobEvents(job.ConfigEventScope, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d, want exactly one config.updated", len(events))
	}
	ev := events[0]
	if ev.Type != job.EventConfigUpdated {
		t.Fatalf("type=%q, want %q", ev.Type, job.EventConfigUpdated)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(ev.Detail), &detail); err != nil {
		t.Fatalf("decode detail %q: %v", ev.Detail, err)
	}
	if len(detail) != 4 {
		t.Fatalf("detail=%v, want exactly {section,key,by,fields}", detail)
	}
	if detail["section"] != "agents" || detail["key"] != "telemetry" || detail["by"] != "web-admin" {
		t.Fatalf("detail=%v", detail)
	}
	fields, _ := detail["fields"].([]any)
	got := make([]string, 0, len(fields))
	for _, f := range fields {
		got = append(got, fmt.Sprint(f))
	}
	if !contains(got, "command") || !contains(got, "type") {
		t.Fatalf("fields=%v, want the applied field names", got)
	}
	if strings.Contains(ev.Detail, "mysecretcmd") {
		t.Fatalf("detail=%q leaks a value", ev.Detail)
	}
}

// bodyText drains a response body for a failure message.
func bodyText(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b := new(bytes.Buffer)
	if _, err := b.ReadFrom(resp.Body); err != nil {
		return "<unreadable>"
	}
	return b.String()
}

// TestDirLockFieldsAreHotEditable (S2, 2026-09-23): dir_lock and agent_health were
// classified restart-required while both are in fact read where they are USED —
// dir_lock once per submit (resolveDirExclusive) and agent_health per health read
// (EffectiveAgentHealth: the /v1/agents view, the pre-dispatch check, the fallback
// decision). A console write must therefore be accepted AND be visible to exactly
// those readers without a restart, which is what this test asserts on — the live
// config generation the dispatcher/health path holds, not just the file or the view.
func TestDirLockFieldsAreHotEditable(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, cr, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	// Both start unset: the documented defaults apply (dir_lock ON).
	if !cr.Jobs.Config().EffectiveDirLock() {
		t.Fatal("dir_lock should default to ON before any write")
	}

	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"dir_lock": false,
		"agent_health": map[string]any{
			"window_sec": 120, "degraded_after": 5, "recover_after_ok": 2,
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT server dir_lock/agent_health status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	var written configWriteResp
	decode(t, resp, &written)
	if contains(written.RestartRequired, "server.dir_lock") || contains(written.RestartRequired, "server.agent_health") {
		t.Fatalf("restart_required=%v, want neither dir_lock nor agent_health (both are hot)", written.RestartRequired)
	}

	// End to end through the readers: the config generation the JOB path resolves
	// against flips at once...
	if cr.Jobs.Config().EffectiveDirLock() {
		t.Fatal("dir_lock=false did not reach the generation resolveDirExclusive reads")
	}
	// ...and so does the one the health classification reads.
	if got := cr.Jobs.Config().EffectiveAgentHealth(); got.WindowSec != 120 || got.DegradedAfter != 5 || got.RecoverAfterOK != 2 {
		t.Fatalf("EffectiveAgentHealth=%+v, want 120/5/2", got)
	}

	// The console reads both back (a pointer, so "unset" stays distinguishable).
	v := getConfigView(t, s, adminToken)
	if v.Server.DirLock == nil || *v.Server.DirLock {
		t.Fatalf("view dir_lock=%v, want false", v.Server.DirLock)
	}
	if v.Server.AgentHealth == nil || v.Server.AgentHealth.WindowSec != 120 {
		t.Fatalf("view agent_health=%+v, want window_sec 120", v.Server.AgentHealth)
	}
	// The policy view must stop badging them as needing a restart — the console
	// builds its form and its badges from this, not from the table directly.
	if p := v.ServerPolicy["dir_lock"]; !p.Editable || p.RestartRequired {
		t.Fatalf("policy dir_lock=%+v, want editable and not restart-required", p)
	}
	if p := v.ServerPolicy["agent_health"]; !p.Editable || p.RestartRequired {
		t.Fatalf("policy agent_health=%+v, want editable and not restart-required", p)
	}
	// And the write transaction saved before reloading.
	if disk := string(readFile(t, cfgPath)); !strings.Contains(disk, "dir_lock: false") {
		t.Fatalf("config file has no dir_lock: false:\n%s", disk)
	}
}

// TestCompoundServerBlocksDecodeSnakeCase pins the decoder the four snake_case
// compound server blocks go through (S2, 2026-09-23): the write body speaks
// `interval_seconds` / `max_file_bytes` / `min_interval_sec` / `window_sec`, while the
// config structs carry only yaml tags — encoding/json matches neither, so decoding
// straight into them silently produced an EMPTY block: the write reported success and
// the setting was gone. It was found while making agent_health hot-editable (its
// sibling in the same switch). The round trip through the live config AND the file is
// what proves the values survive a save+reload.
func TestCompoundServerBlocksDecodeSnakeCase(t *testing.T) {
	yamlText, _, _ := configWriteFixture(t)
	s, cr, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})

	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"runner_probe":    map[string]any{"interval_seconds": 45, "timeout_seconds": 7},
		"skill_limits":    map[string]any{"max_file_bytes": 1048576, "max_total_bytes": 8388608},
		"comment_trigger": map[string]any{"min_interval_sec": 30, "max_per_scope": 5},
		"agent_health":    map[string]any{"window_sec": 900, "degraded_after": 4, "recover_after_ok": 2},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}

	live := cr.Jobs.Config().Server
	if live.RunnerProbe.IntervalSeconds != 45 || live.RunnerProbe.TimeoutSeconds != 7 {
		t.Fatalf("runner_probe=%+v, want 45/7", live.RunnerProbe)
	}
	if live.SkillLimits.MaxFileBytes != 1048576 || live.SkillLimits.MaxTotalBytes != 8388608 {
		t.Fatalf("skill_limits=%+v, want 1MiB/8MiB", live.SkillLimits)
	}
	if live.CommentTrigger.MinIntervalSec == nil || *live.CommentTrigger.MinIntervalSec != 30 ||
		live.CommentTrigger.MaxPerScope == nil || *live.CommentTrigger.MaxPerScope != 5 {
		t.Fatalf("comment_trigger=%+v, want 30/5", live.CommentTrigger)
	}
	if live.AgentHealth == nil || live.AgentHealth.WindowSec != 900 ||
		live.AgentHealth.DegradedAfter != 4 || live.AgentHealth.RecoverAfterOK != 2 {
		t.Fatalf("agent_health=%+v, want 900/4/2", live.AgentHealth)
	}

	// The console reads them back from the view it builds its form from.
	v := getConfigView(t, s, adminToken)
	if v.Server.RunnerProbe.IntervalSeconds != 45 || v.Server.SkillLimits.MaxTotalBytes != 8388608 {
		t.Fatalf("view runner_probe=%+v skill_limits=%+v", v.Server.RunnerProbe, v.Server.SkillLimits)
	}
	if v.Server.CommentTrigger.MinIntervalSec == nil || *v.Server.CommentTrigger.MinIntervalSec != 30 {
		t.Fatalf("view comment_trigger=%+v, want 30", v.Server.CommentTrigger)
	}

	// And they reached the file: a save that dropped them would come back as defaults
	// on the next reload — the very failure this test exists for.
	disk := string(readFile(t, cfgPath))
	for _, want := range []string{"interval_seconds: 45", "max_file_bytes: 1048576", "min_interval_sec: 30", "window_sec: 900"} {
		if !strings.Contains(disk, want) {
			t.Fatalf("config file is missing %q:\n%s", want, disk)
		}
	}

	// `agent_health: null` clears the block (unset = the documented defaults), which
	// is a different decision from a block of zeros.
	resp = do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{"agent_health": nil})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT agent_health=null status=%d, want 200: %s", resp.StatusCode, bodyText(t, resp))
	}
	if cr.Jobs.Config().Server.AgentHealth != nil {
		t.Fatalf("agent_health=%+v after null, want nil", cr.Jobs.Config().Server.AgentHealth)
	}
}
