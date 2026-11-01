package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

const synthAnswer = `{"schema":"daily-memory-synthesis.v1","summary":"one concrete thing happened","highlights":[],"open_questions":[]}`

type synthRig struct {
	s       *Server
	capture string
}

// synthServer is a test server whose "codex" cli-agent is the test helper: it records the
// prompt it was given and prints `answer`, standing in for the model.
func synthServer(t *testing.T, enabled bool, answer string) synthRig {
	t.Helper()
	bin := testcmd.Path(t)
	capture := filepath.Join(t.TempDir(), "prompt.txt")
	s := newTestServerCfgWith(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "lim", Token: ceilTokLim, Ceiling: &config.CallerCeiling{Projects: []string{"p1"}}},
			{ID: "plain", Token: ceilTokPlain},
		},
	}, func(cfg *config.Config) {
		for _, p := range []string{"p1", "p2"} {
			cfg.Projects[p] = config.ProjectConfig{HostPath: cfg.Storage.Root, AllowedAgents: []string{"codex"}, AllowedRunners: []string{"local"}}
		}
		cfg.Agents = map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"prompt-capture", capture, answer, "{{prompt}}"}},
		}
		cfg.Work.SummarizerAgent = "codex"
		cfg.Memory.Synthesis = config.SynthesisConfig{Enabled: enabled, Timeout: "1m"}
	})
	for _, p := range []string{"p1", "p2"} {
		if _, err := s.jobs.Meta().CreateWorkItem(jobstore.WorkItemInput{Title: "ship it in " + p, ProjectKey: p, Status: jobstore.WorkParked, By: "human:t"}); err != nil {
			t.Fatal(err)
		}
	}
	return synthRig{s: s, capture: capture}
}

func (r synthRig) post(t *testing.T, token string, body any) (int, string) {
	t.Helper()
	return ceilReq(t, r.s, token, http.MethodPost, "/v1/memory-syntheses/run", body)
}

type synthRunResp struct {
	Outcome string                      `json:"outcome"`
	Run     jobstore.MemorySynthesisRun `json:"run"`
}

func decodeSynthRun(t *testing.T, body string) synthRunResp {
	t.Helper()
	var out synthRunResp
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

// POST /v1/memory-syntheses/run drives the real wiring: it runs, reuses an unchanged day,
// re-runs on force, and the run (with its result) is readable through the read routes.
func TestRunMemorySynthesisEndpointRunsReusesAndForces(t *testing.T) {
	r := synthServer(t, true, synthAnswer)
	grantAll(r.s)

	status, body := r.post(t, ceilTokPlain, map[string]any{"project": "p1"})
	first := decodeSynthRun(t, body)
	if status != http.StatusOK || first.Outcome != "succeeded" || !strings.Contains(first.Run.ResultJSON, "one concrete thing happened") {
		t.Fatalf("first = %d %s", status, body)
	}
	if first.Run.CreatedBy != "human:plain" || first.Run.ProjectKey != "p1" {
		t.Fatalf("run=%+v", first.Run)
	}

	// The detail route shows the same result; the list stays metadata only.
	status, body = ceilReq(t, r.s, ceilTokPlain, http.MethodGet, "/v1/memory-syntheses/"+first.Run.ID, nil)
	var detail jobstore.MemorySynthesisRun
	_ = json.Unmarshal([]byte(body), &detail)
	if status != http.StatusOK || detail.ResultJSON != first.Run.ResultJSON {
		t.Fatalf("detail = %d %s", status, body)
	}

	// Same input: reused, and the model is not called again.
	if err := os.Remove(r.capture); err != nil {
		t.Fatal(err)
	}
	status, body = r.post(t, ceilTokPlain, map[string]any{"project": "p1"})
	again := decodeSynthRun(t, body)
	if status != http.StatusOK || again.Outcome != "reused" || again.Run.ID != first.Run.ID {
		t.Fatalf("again = %d %s", status, body)
	}
	if _, err := os.Stat(r.capture); err == nil {
		t.Fatal("a reused run must not call the model")
	}

	// force: a new run, the old one is retired.
	status, body = r.post(t, ceilTokPlain, map[string]any{"project": "p1", "force": true})
	forced := decodeSynthRun(t, body)
	if status != http.StatusOK || forced.Outcome != "succeeded" || forced.Run.ID == first.Run.ID {
		t.Fatalf("forced = %d %s", status, body)
	}
	if _, err := os.Stat(r.capture); err != nil {
		t.Fatal("a forced run must call the model")
	}

	// A day without material answers "empty" and creates no record.
	status, body = r.post(t, ceilTokPlain, map[string]any{"project": "p1", "day": "2001-01-02"})
	if status != http.StatusOK || decodeSynthRun(t, body).Outcome != "empty" || strings.Contains(body, `"run"`) {
		t.Fatalf("empty = %d %s", status, body)
	}
}

// A model answer that cannot be parsed is a recorded failed run with the reason, not an HTTP error.
func TestRunMemorySynthesisEndpointRecordsFailure(t *testing.T) {
	r := synthServer(t, true, "no json here")
	grantAll(r.s)
	status, body := r.post(t, ceilTokPlain, map[string]any{"project": "p1"})
	out := decodeSynthRun(t, body)
	if status != http.StatusOK || out.Outcome != "failed" || out.Run.Status != "failed" || !strings.Contains(out.Run.Error, "invalid output") || out.Run.ResultJSON != "" {
		t.Fatalf("failure = %d %s", status, body)
	}
}

func TestRunMemorySynthesisEndpointRefusals(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		r := synthServer(t, false, synthAnswer)
		grantAll(r.s)
		status, body := r.post(t, ceilTokPlain, map[string]any{"project": "p1"})
		if status != http.StatusConflict || !strings.Contains(body, "memory.synthesis.enabled") {
			t.Fatalf("disabled = %d %s", status, body)
		}
		if _, err := os.Stat(r.capture); err == nil {
			t.Fatal("a disabled synthesis must not call the model")
		}
		if rows, _ := r.s.jobs.Meta().ListMemorySynthesisRuns(jobstore.MemorySynthesisRunFilter{}); len(rows) != 0 {
			t.Fatalf("rows=%+v", rows)
		}
	})
	t.Run("bad requests", func(t *testing.T) {
		r := synthServer(t, true, synthAnswer)
		grantAll(r.s)
		for name, body := range map[string]any{
			"no project":      map[string]any{},
			"unknown project": map[string]any{"project": "nope"},
			"bad day":         map[string]any{"project": "p1", "day": "28/10/2026"},
		} {
			if status, resp := r.post(t, ceilTokPlain, body); status != http.StatusBadRequest {
				t.Errorf("%s = %d %s", name, status, resp)
			}
		}
	})
	t.Run("missing permission", func(t *testing.T) {
		r := synthServer(t, true, synthAnswer)
		status, body := r.post(t, ceilTokPlain, map[string]any{"project": "p1"})
		if status != http.StatusForbidden || !strings.Contains(body, authz.PermMemorySynthesisRun) {
			t.Fatalf("no grant = %d %s", status, body)
		}
		if _, err := os.Stat(r.capture); err == nil {
			t.Fatal("a refused request must not call the model")
		}
	})
	t.Run("project outside the ceiling", func(t *testing.T) {
		r := synthServer(t, true, synthAnswer)
		grantAll(r.s)
		if status, body := r.post(t, ceilTokLim, map[string]any{"project": "p2"}); status == http.StatusOK {
			t.Fatalf("out-of-scope project must be refused: %d %s", status, body)
		}
		if _, err := os.Stat(r.capture); err == nil {
			t.Fatal("an out-of-scope request must not call the model")
		}
		if status, body := r.post(t, ceilTokLim, map[string]any{"project": "p1"}); status != http.StatusOK {
			t.Fatalf("in-scope project = %d %s", status, body)
		}
	})
	t.Run("job credential", func(t *testing.T) {
		r := synthServer(t, true, synthAnswer)
		grantAll(r.s)
		member := seedJobToken(t, r.s, "job-syn-run", jobstore.JobCredentialMember, "")
		if status, body := r.post(t, member, map[string]any{"project": "p1"}); status != http.StatusForbidden {
			t.Fatalf("job credential = %d %s", status, body)
		}
		if _, err := os.Stat(r.capture); err == nil {
			t.Fatal("a job credential must not trigger a synthesis")
		}
	})
}
