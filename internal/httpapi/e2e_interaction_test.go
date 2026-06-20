package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// wrapperScript is a real cli-agent wrapper: it raises a question interaction
// over the bridge HTTP API, polls until an answer appears, then echoes the
// answer and exits 0. sed parses the single-interaction JSON reliably.
const wrapperScript = `#!/bin/sh
set -e
JOB_ID="$1"
# 1) raise a question interaction
curl -s -X POST -H "Authorization: Bearer $BRIDGE_TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"question","prompt":"need input"}' \
  "$BRIDGE_BASE/v1/jobs/$JOB_ID/interactions" >/dev/null
# 2) poll until answered, then echo the answer and finish
i=0
while [ "$i" -lt 100 ]; do
  body=$(curl -s -H "Authorization: Bearer $BRIDGE_TOKEN" "$BRIDGE_BASE/v1/jobs/$JOB_ID/interactions")
  ans=$(printf '%s' "$body" | sed -n 's/.*"answer":"\([^"]*\)".*/\1/p')
  if [ -n "$ans" ]; then echo "ANSWER=$ans"; exit 0; fi
  i=$((i+1)); sleep 0.1
done
echo "no-answer"; exit 1
`

// TestE2EInteractionWrapper drives a real child-process cli-agent wrapper (sh +
// curl) through the full interaction loop over HTTP: wrapper raises a question
// -> user (test) sees a pending interaction via GET -> user answers via POST ->
// wrapper reads the answer and completes the job. This exercises the P9 contract
// against actual subprocess + HTTP, not an in-Go simulation.
func TestE2EInteractionWrapper(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	root := t.TempDir()
	scriptPath := filepath.Join(root, "wrapper.sh")
	if err := os.WriteFile(scriptPath, []byte(wrapperScript), 0o755); err != nil {
		t.Fatalf("write wrapper script: %v", err)
	}

	// storageRoot isolates job results from the project host path so the wrapper
	// script file isn't mistaken for a result artifact.
	storageRoot := t.TempDir()
	cfg := &config.Config{
		Server:  config.ServerConfig{Token: testToken},
		Storage: config.StorageConfig{Root: storageRoot},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root, // cwd "." resolves here
				AllowedAgents:  []string{"wrapper"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"wrapper": {
				Type:    agent.TypeCLIAgent,
				Command: "sh",
				// {{job_id}} is rendered to the real job id by agent.Render.
				Args: []string{scriptPath, "{{job_id}}"},
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, storageRoot), nil)
	srv := New(&cfg.Server, testToken, false, jobs, projects, agents, nil, nil, nil, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// The wrapper subprocess inherits os.Environ via the local runner, so the
	// bridge base URL/token reach it through the environment.
	t.Setenv("BRIDGE_BASE", ts.URL)
	t.Setenv("BRIDGE_TOKEN", testToken)

	client := ts.Client()

	// 1) submit the cli-agent job over HTTP. prompt is non-empty (cli-agent
	//    requires it); {{job_id}} in the wrapper args renders to the real id.
	jobID := e2ePost(t, client, ts.URL+"/v1/jobs", map[string]any{
		"project_key": "self",
		"agent":       "wrapper",
		"runner":      "local",
		"prompt":      "go",
		"cwd":         ".",
		"timeout_sec": 30,
	})["id"].(string)
	if jobID == "" {
		t.Fatalf("submit returned no job id")
	}

	overall := time.Now().Add(20 * time.Second)

	// 2) user side: poll until a pending interaction appears, then answer it.
	var iid string
	for time.Now().Before(overall) {
		for _, it := range e2eListInteractions(t, client, ts.URL, jobID) {
			if it["status"] == "pending" {
				iid, _ = it["id"].(string)
				break
			}
		}
		if iid != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if iid == "" {
		dumpJob(t, client, ts.URL, jobID)
		t.Fatalf("no pending interaction appeared in time")
	}

	e2ePost(t, client, ts.URL+"/v1/jobs/"+jobID+"/interactions/"+iid+"/answer",
		map[string]any{"answer": "hi-from-user"})

	// 3) wait for the job to finish; it should complete successfully because the
	//    wrapper read the answer and exited 0.
	var final map[string]any
	for time.Now().Before(overall) {
		final = e2eGetJob(t, client, ts.URL, jobID)
		switch final["status"] {
		case "done", "failed", "cancelled", "timeout":
			goto terminal
		}
		time.Sleep(50 * time.Millisecond)
	}
terminal:
	if final["status"] != "done" {
		dumpJob(t, client, ts.URL, jobID)
		t.Fatalf("job status=%v, want done", final["status"])
	}

	// 4) stdout must carry the answer the wrapper echoed after reading it.
	stdout := e2eGetText(t, client, ts.URL+"/v1/jobs/"+jobID+"/logs/stdout")
	if !strings.Contains(stdout, "ANSWER=hi-from-user") {
		t.Fatalf("stdout missing wrapper answer echo; got: %q", stdout)
	}

	// 5) the interaction must be recorded as answered.
	var foundAnswered bool
	for _, it := range e2eListInteractions(t, client, ts.URL, jobID) {
		if it["id"] == iid {
			if it["status"] != "answered" || it["answer"] != "hi-from-user" {
				t.Fatalf("interaction not answered correctly: %+v", it)
			}
			foundAnswered = true
		}
	}
	if !foundAnswered {
		t.Fatalf("answered interaction %s not found in final list", iid)
	}
}

// e2ePost POSTs a JSON body with the bearer token and returns the decoded JSON
// object response. It fails the test on transport/status errors.
func e2ePost(t *testing.T, client *http.Client, url string, body map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s status=%d body=%s", url, resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode POST %s: %v", url, err)
	}
	return out
}

// e2eGetJob fetches the job state object.
func e2eGetJob(t *testing.T, client *http.Client, base, id string) map[string]any {
	t.Helper()
	return e2eGetJSON(t, client, base+"/v1/jobs/"+id)
}

// e2eListInteractions fetches the interactions array for a job.
func e2eListInteractions(t *testing.T, client *http.Client, base, id string) []map[string]any {
	t.Helper()
	obj := e2eGetJSON(t, client, base+"/v1/jobs/"+id+"/interactions")
	raw, _ := obj["interactions"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// e2eGetJSON GETs url with the bearer token and decodes a JSON object.
func e2eGetJSON(t *testing.T, client *http.Client, url string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status=%d body=%s", url, resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode GET %s: %v", url, err)
	}
	return out
}

// e2eGetText GETs url with the bearer token and returns the raw body text.
func e2eGetText(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// dumpJob logs the job state and both log streams for diagnostics on failure.
func dumpJob(t *testing.T, client *http.Client, base, id string) {
	t.Helper()
	t.Logf("job state: %+v", e2eGetJob(t, client, base, id))
	t.Logf("stdout: %q", e2eGetText(t, client, base+"/v1/jobs/"+id+"/logs/stdout"))
	t.Logf("stderr: %q", e2eGetText(t, client, base+"/v1/jobs/"+id+"/logs/stderr"))
}
