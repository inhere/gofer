package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/trigger"
)

const triggerTestSecret = "trigger-p0-secret"

// triggerFixtureYAML adds two triggers to the config-write fixture: "push" owned by an
// unrestricted caller and "capped" owned by a caller whose ceiling excludes the template's
// agent.
func triggerFixtureYAML(t *testing.T) (yamlText, projectDir string) {
	t.Helper()
	yamlText, hostDir, _ := configWriteFixture(t)
	const callers = `  callers:
    - id: ceil-op
      token: tok-ceil-op
      ceiling:
        agents: [claude]
        runners: [local]
`
	const triggers = `  triggers:
    - name: push
      enabled: true
      owner_caller: web-op
      project: seed
      template: tpl
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
      input: {vars: {ref: body.ref}}
      dedup: {key: "header.X-Delivery"}
    - name: capped
      enabled: true
      owner_caller: ceil-op
      project: seed
      template: tpl
      auth: {secret_env: GOFER_TRIGGER_HTTP_TEST}
`
	yamlText = strings.Replace(yamlText, "  callers:\n", triggers+callers, 1)
	if !strings.Contains(yamlText, "name: capped") {
		t.Fatalf("fixture injection failed:\n%s", yamlText)
	}
	dir := filepath.Join(hostDir, ".gofer", "templates")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tpl.md"), []byte("---\nagent: mytool\nrunner: local\nvars:\n  ref: {desc: git ref}\n---\nrebuild {{ref}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return yamlText, hostDir
}

func signedTriggerRequest(name, body, delivery string) trigger.TriggerRequest {
	ts := "1700000000"
	mac := hmac.New(sha256.New, []byte(triggerTestSecret))
	mac.Write([]byte(ts + "\n" + body))
	return trigger.TriggerRequest{TriggerName: name, RawBody: []byte(body), Headers: map[string]string{
		"X-Gofer-Trigger-Timestamp": ts, "X-Gofer-Trigger-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil)), "X-Delivery": delivery,
	}}
}

func grantUsersSubmit(s *Server, on bool) {
	if !on {
		s.setDBGrants(nil)
		return
	}
	s.setDBGrants(&grantScope{subject: "users", perms: []string{authz.PermJobSubmit}, grants: map[string]string{authz.PermJobSubmit: "g-test"}})
}

// TestTriggerEngineAdmitsThroughTheOwnersLivePermissions drives the production assembly
// (trigger.NewServerEngine over the live config, the job service and the server's
// permission model): a normal invocation is admitted, a revoked owner and an owner whose
// ceiling excludes the template's agent are refused, and revoking takes effect on the very
// next call.
func TestTriggerEngineAdmitsThroughTheOwnersLivePermissions(t *testing.T) {
	yamlText, _ := triggerFixtureYAML(t)
	t.Setenv("GOFER_TRIGGER_HTTP_TEST", triggerTestSecret)
	s, cr, _ := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	eng := trigger.NewServerEngine(cr.Config, cr.Jobs, s.CallerHasPermission)
	s.SetTriggerEngine(eng)
	if s.TriggerEngine() != eng {
		t.Fatal("engine not installed")
	}
	ctx := context.Background()
	// Replay window: the fixed stamp is far in the past, so run the engine on a clock at it.
	eng.SetClock(func() time.Time { return time.Unix(1700000000, 0) })

	grantUsersSubmit(s, true)
	res, err := eng.Invoke(ctx, signedTriggerRequest("push", `{"ref":"main"}`, "d-1"))
	if err != nil || res.JobID == "" {
		t.Fatalf("normal invocation: res=%+v err=%v", res, err)
	}
	if rec, ok, _ := cr.Jobs.Meta().GetJob(res.JobID); !ok || rec.OriginCaller != "web-op" || rec.Channel != "trigger" {
		t.Fatalf("job origin=%q channel=%q ok=%v", rec.OriginCaller, rec.Channel, ok)
	}

	// Ceiling: ceil-op may only use claude, the template picks mytool.
	_, err = eng.Invoke(ctx, signedTriggerRequest("capped", `{}`, "d-2"))
	if !errors.Is(err, job.ErrCeilingDenied) {
		t.Fatalf("ceiling mismatch err=%v, want ErrCeilingDenied", err)
	}

	// Revocation: the next call is refused, with no job created.
	grantUsersSubmit(s, false)
	before := cr.Jobs.Stats().InFlight
	_, err = eng.Invoke(ctx, signedTriggerRequest("push", `{"ref":"dev"}`, "d-3"))
	if !errors.Is(err, trigger.ErrOwnerDenied) {
		t.Fatalf("revoked owner err=%v, want ErrOwnerDenied", err)
	}
	if cr.Jobs.Stats().InFlight > before {
		t.Fatal("a job was created for a revoked owner")
	}
	// The refusal did not consume the dedup claim: re-granting makes the same event work.
	grantUsersSubmit(s, true)
	if res, err := eng.Invoke(ctx, signedTriggerRequest("push", `{"ref":"dev"}`, "d-3")); err != nil || res.JobID == "" || res.Duplicate {
		t.Fatalf("after re-grant: res=%+v err=%v", res, err)
	}
}

// TestTriggersAreFileOnly pins design 3.1: server.triggers can only be changed in the
// config file. The write and dry-run endpoints refuse it and an unrelated server write
// keeps the file's triggers.
func TestTriggersAreFileOnly(t *testing.T) {
	yamlText, _ := triggerFixtureYAML(t)
	s, cr, cfgPath := newConfigWriteTestServer(t, yamlText, agent.NoopDetector{})
	before := readFile(t, cfgPath)
	resp := do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{
		"triggers": []map[string]any{{"name": "pwn", "enabled": true, "owner_caller": "web-op", "project": "seed", "template": "tpl", "auth": map[string]any{"secret_env": "X"}}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT triggers status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Detail != "field not editable: server.triggers" {
		t.Fatalf("detail=%q", body.Detail)
	}
	if string(before) != string(readFile(t, cfgPath)) {
		t.Fatal("a refused triggers write changed the file")
	}
	resp = do(t, s, http.MethodPost, "/v1/config/validate", adminToken, map[string]any{"section": "server", "value": map[string]any{"triggers": []any{}}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("validate triggers status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	resp = do(t, s, http.MethodPut, "/v1/config/server", adminToken, map[string]any{"notification": map[string]any{"enabled": false}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT notification status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	for name, trs := range map[string][]config.TriggerConfig{"live": cr.Jobs.Config().Server.Triggers, "file": loadTriggers(t, cfgPath)} {
		if len(trs) != 2 || trs[0].Name != "push" || trs[0].Auth.SecretEnv != "GOFER_TRIGGER_HTTP_TEST" {
			t.Fatalf("%s triggers after an unrelated write=%+v", name, trs)
		}
	}
	// The read view carries no trigger secret material.
	if raw := bodyText(t, do(t, s, http.MethodGet, "/v1/config", adminToken, nil)); strings.Contains(raw, triggerTestSecret) {
		t.Fatal("GET /v1/config echoed a trigger secret")
	}
}

func loadTriggers(t *testing.T, path string) []config.TriggerConfig {
	t.Helper()
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload %s: %v", path, err)
	}
	return cfg.Server.Triggers
}
