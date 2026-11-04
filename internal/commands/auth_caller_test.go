package commands

// `gofer auth caller` 的命令层测试（配置入库 P1，计划 T7）：
//
//   - the ceiling rule is checked locally too: a caller with neither a `--ceiling-*` block nor
//     `--no-ceiling` is refused here with the flags in the message (the server refuses it as a
//     400 as well), and the request really goes through `app.Run` (GR110);
//   - the token is printed once and the caller's ceiling reaches the request body;
//   - `repair` refuses to run without `--offline` and never creates a database.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// callerStubServer serves the caller routes: it records every request and answers a fixed
// caller change, so the printed output is what a real server's answer produces.
type callerStubServer struct {
	requests []callerStubRequest
	body     map[string]any
}

// isolateCallerFlags restores the package-level caller option struct between cases: gcli binds
// straight into it, so a flag one case passes is still there for the next one (G053).
func isolateCallerFlags(t *testing.T) {
	t.Helper()
	resetFlagGlobals()
	t.Cleanup(resetFlagGlobals)
}

type callerStubRequest struct {
	method, path, body string
}

func (s *callerStubServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.requests = append(s.requests, callerStubRequest{method: r.Method, path: r.URL.Path, body: string(raw)})
		w.WriteHeader(http.StatusCreated)
		body := s.body
		if body == nil {
			body = map[string]any{
				"caller": map[string]any{"id": "ci", "source": "db", "state": "active", "token_set": true, "token_hint": "gct_…abcd"},
				"token":  "gct_minted-token",
			}
		}
		_ = json.NewEncoder(w).Encode(body)
	})
}

// TestAuthCallerAddThroughAppRunIsRefusedWithoutACeiling (GR110): the real gcli app is driven, so
// the flag binding, the local rule and the exit code are the ones a user gets. No server is
// contacted: the command refuses before it builds a request.
func TestAuthCallerAddThroughAppRunIsRefusedWithoutACeiling(t *testing.T) {
	isolateCallerFlags(t)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"auth", "caller", "add", "ci", "--server", "http://127.0.0.1:1"})
	})
	if code == 0 {
		t.Fatalf("add without a ceiling must fail, out=%s", out)
	}
	if !strings.Contains(out, "--no-ceiling") || !strings.Contains(out, "--ceiling-project") {
		t.Fatalf("the refusal must name both ways: %s", out)
	}
}

// TestAuthCallerAddSendsTheCeilingAndPrintsTheTokenOnce: the --ceiling-* flags become the
// request body's ceiling block, the omission of --ceiling-runner stays visible in it (the server
// then refuses the machine runner), and the token is printed with the one-time warning.
func TestAuthCallerAddSendsTheCeilingAndPrintsTheTokenOnce(t *testing.T) {
	isolateAgentSessionEnv(t)
	isolateCallerFlags(t)
	stub := &callerStubServer{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{
			"auth", "caller", "add", "ci",
			"--server", srv.URL, "--token", "tok-root",
			"--name", "CI runner",
			"--ceiling-project", "p1", "--ceiling-project", "p2",
			"--ceiling-allow-exec",
		})
	})
	if code != 0 {
		t.Fatalf("add exit code=%d, out=%s", code, out)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %+v, want one", stub.requests)
	}
	req := stub.requests[0]
	if req.method != http.MethodPost || req.path != "/v1/callers" {
		t.Fatalf("request = %s %s, want POST /v1/callers", req.method, req.path)
	}
	var body struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Ceiling struct {
			Projects  []string `json:"projects"`
			Runners   []string `json:"runners"`
			AllowExec *bool    `json:"allow_exec"`
		} `json:"ceiling"`
		NoCeiling bool `json:"no_ceiling"`
	}
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatalf("decode the request body %q: %v", req.body, err)
	}
	if body.ID != "ci" || body.Name != "CI runner" {
		t.Fatalf("body id/name = %q/%q", body.ID, body.Name)
	}
	if len(body.Ceiling.Projects) != 2 || body.Ceiling.Projects[1] != "p2" {
		t.Fatalf("ceiling projects = %v, want p1 and p2", body.Ceiling.Projects)
	}
	if len(body.Ceiling.Runners) != 0 {
		t.Fatalf("ceiling runners = %v, want none (the machine runner is then refused)", body.Ceiling.Runners)
	}
	if body.Ceiling.AllowExec == nil || !*body.Ceiling.AllowExec {
		t.Fatalf("ceiling allow_exec = %v, want true", body.Ceiling.AllowExec)
	}
	if body.NoCeiling {
		t.Fatal("no_ceiling must not be sent when a ceiling block is given")
	}
	if !strings.Contains(out, "gct_minted-token") {
		t.Fatalf("the token must be printed: %s", out)
	}
	if !strings.Contains(out, "only time the token is shown") {
		t.Fatalf("the one-time note must be printed: %s", out)
	}
}

// TestAuthCallerNoCeilingSendsTheFlag: `--no-ceiling` is the other form, and it must not be
// dropped on the way out.
func TestAuthCallerNoCeilingSendsTheFlag(t *testing.T) {
	isolateAgentSessionEnv(t)
	isolateCallerFlags(t)
	stub := &callerStubServer{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{
			"auth", "caller", "add", "bot", "--server", srv.URL, "--token", "tok-root", "--no-ceiling",
		})
	})
	if code != 0 {
		t.Fatalf("add exit code=%d, out=%s", code, out)
	}
	if len(stub.requests) != 1 || !strings.Contains(stub.requests[0].body, `"no_ceiling":true`) {
		t.Fatalf("the request must carry no_ceiling: %+v", stub.requests)
	}
}

// TestAuthCallerRepairNeedsOffline: the repair is the one caller command that does not go
// through the server, so the flag that says so is required.
func TestAuthCallerRepairNeedsOffline(t *testing.T) {
	isolateAgentSessionEnv(t)
	isolateCallerFlags(t)
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"auth", "caller", "repair", "ci"})
	})
	if code == 0 || !strings.Contains(out, "--offline") {
		t.Fatalf("repair without --offline: code=%d, out=%s", code, out)
	}
}

// TestAuthCallerRepairDoesNotCreateADatabase: pointing it at a config whose database does not
// exist must fail without creating the file (D5).
func TestAuthCallerRepairDoesNotCreateADatabase(t *testing.T) {
	isolateCallerFlags(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	dbPath := filepath.ToSlash(filepath.Join(dir, "missing.db"))
	text := "storage:\n  root: " + filepath.ToSlash(dir) + "\n  db_path: " + dbPath + "\n"
	if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
		t.Fatalf("write the config: %v", err)
	}
	var code int
	out := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"auth", "caller", "repair", "ci", "--offline", "-c", cfgPath})
	})
	if code == 0 {
		t.Fatalf("repair against a missing database must fail, out=%s", out)
	}
	if !strings.Contains(out, "missing.db") {
		t.Fatalf("the error must name the database path: %s", out)
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("repair must not create the database")
	}
}
