package job

import (
	"encoding/json"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/secret"
)

func TestRedactedRequestScrubsEnvAgentArgsAndClearsReadNoise(t *testing.T) {
	s := newTestService(t, t.TempDir())
	raw, err := json.Marshal(JobRequest{
		ProjectKey:   "self",
		Agent:        "codex",
		Runner:       "local",
		Prompt:       "use token=sk-test-prompt",
		SystemPrompt: "api_key: sk-test-system",
		AgentArgs:    []string{"--api-key=sk-test-agent"},
		Cmd:          []string{"tool", "--token=sk-test-cmd"},
		Cwd:          "auth=sk-test-cwd",
		Env:          map[string]string{"API_TOKEN": "sk-test-env", "EMPTY": ""},
		RequestID:    "request-1",
		CallerID:     "caller-1",
		SessionID:    "session-1",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "job-1", ProjectKey: "self", Agent: "codex", Runner: "local",
		Status: "done", ResultDir: t.TempDir(), RequestJSON: string(raw),
		StartedAt: 1, UpdatedAt: 2,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	req, ok, redacted, err := s.RedactedRequest("job-1")
	if err != nil {
		t.Fatalf("RedactedRequest: %v", err)
	}
	if !ok || !redacted {
		t.Fatalf("RedactedRequest ok/redacted = %v/%v, want true/true", ok, redacted)
	}
	if req.Env["API_TOKEN"] != secret.Placeholder {
		t.Fatalf("env secret = %q, want placeholder", req.Env["API_TOKEN"])
	}
	if req.Env["EMPTY"] != "" {
		t.Fatalf("empty env value should stay empty, got %q", req.Env["EMPTY"])
	}
	if req.AgentArgs[0] != "--api-key="+secret.Placeholder {
		t.Fatalf("agent_args not redacted: %#v", req.AgentArgs)
	}
	if req.Cmd[1] != "--token="+secret.Placeholder {
		t.Fatalf("cmd not redacted: %#v", req.Cmd)
	}
	if req.Prompt != "use token="+secret.Placeholder {
		t.Fatalf("prompt not redacted: %q", req.Prompt)
	}
	if req.SystemPrompt != "api_key: "+secret.Placeholder {
		t.Fatalf("system_prompt not redacted: %q", req.SystemPrompt)
	}
	if req.Cwd != "auth="+secret.Placeholder {
		t.Fatalf("cwd not redacted: %q", req.Cwd)
	}
	if req.RequestID != "" || req.CallerID != "" || req.SessionID != "" {
		t.Fatalf("read-noise fields not cleared: request=%q caller=%q session=%q", req.RequestID, req.CallerID, req.SessionID)
	}
}

func TestRedactedRequestNoSecretAndUnknown(t *testing.T) {
	s := newTestService(t, t.TempDir())
	raw, err := json.Marshal(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := s.meta.UpsertJob(jobstore.JobRecord{
		ID: "job-plain", ProjectKey: "self", Agent: "exec", Runner: "local",
		Status: "done", ResultDir: t.TempDir(), RequestJSON: string(raw),
		StartedAt: 1, UpdatedAt: 2,
	}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}

	req, ok, redacted, err := s.RedactedRequest("job-plain")
	if err != nil {
		t.Fatalf("RedactedRequest plain: %v", err)
	}
	if !ok || redacted {
		t.Fatalf("plain ok/redacted = %v/%v, want true/false", ok, redacted)
	}
	if len(req.Cmd) != 2 || req.Cmd[0] != "go" || req.Cmd[1] != "version" {
		t.Fatalf("plain request changed: %+v", req)
	}

	_, ok, redacted, err = s.RedactedRequest("missing")
	if err != nil {
		t.Fatalf("RedactedRequest missing: %v", err)
	}
	if ok || redacted {
		t.Fatalf("missing ok/redacted = %v/%v, want false/false", ok, redacted)
	}
}
