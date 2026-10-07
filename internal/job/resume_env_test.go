package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newFakeNDJSONResumeService wires a cli-agent whose CLI emits an NDJSON stream and
// reports a few env variables in its final answer: first turn and resume template
// both run the same helper, so any difference between the two turns comes from how
// gofer launched them.
func newFakeNDJSONResumeService(t *testing.T, root string) *Service {
	t.Helper()
	bin := testcmd.Path(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"fake-cli"},
				AllowedRunners: []string{"local"},
				// allow_exec stays false: a resume must not need it.
			},
		},
		Agents: map[string]config.AgentConfig{
			"fake-cli": {
				Type:    agent.TypeCLIAgent,
				Command: bin,
				Args:    []string{"fake-ndjson-agent", "FAKE_HOME,FAKE_MODEL,FAKE_JOB,FAKE_FILE,FAKE_OVR", "{{prompt}}"},
				SessionResume: []string{
					"fake-ndjson-agent", "FAKE_HOME,FAKE_MODEL,FAKE_JOB,FAKE_FILE,FAKE_OVR", "--resume", "{{session_id}}", "{{prompt}}",
				},
				Env:              map[string]string{"FAKE_HOME": "/agent/home", "FAKE_MODEL": "model-a"},
				OutputFormat:     config.OutputFormatNDJSON,
				NDJSONKeep:       []string{"session", "result"},
				NDJSONStdout:     config.NDJSONStdoutFinalText,
				NDJSONStdoutPath: "result",
			},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

func readJobStdout(t *testing.T, root, id string) string {
	t.Helper()
	b, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(id, store.StreamStdout, 0)
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	return string(b)
}

// A resumed cli-agent runs through the exec carrier but must behave like the first
// turn: same output projection (final answer only in stdout.log) and the same
// environment (agent env + the source job's env and env_files), with the resume
// request's own env winning — and none of the inherited values stored in the new
// request_json.
func TestResumeCarrierInheritsOutputProjectionAndEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "creds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "creds", "app.env"), []byte("FAKE_FILE=file-secret-value\nFAKE_OVR=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newFakeNDJSONResumeService(t, root)

	first := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "fake-cli", Runner: "local",
		Prompt: "first", Cwd: ".", TimeoutSec: 30,
		Env:      map[string]string{"FAKE_JOB": "job-secret-value", "FAKE_OVR": "from-source-job"},
		EnvFiles: []string{"creds/app.env"},
	})
	if first.Status != StatusDone {
		t.Fatalf("first: status=%s err=%s", first.Status, first.Error)
	}
	wantFirst := "final:FAKE_HOME=/agent/home;FAKE_MODEL=model-a;FAKE_JOB=job-secret-value;FAKE_FILE=file-secret-value;FAKE_OVR=from-source-job\n"
	if got := readJobStdout(t, root, first.ID); got != wantFirst {
		t.Fatalf("first stdout = %q, want %q", got, wantFirst)
	}
	if first.SessionID == "" {
		t.Fatalf("first turn captured no session id")
	}

	// Plain resume: identical answer, identical env.
	r1, err := s.ResumeJob(first.ID, "second", "", "caller")
	if err != nil {
		t.Fatal(err)
	}
	r1, _ = s.Wait(r1.ID)
	if r1.Status != StatusDone {
		t.Fatalf("resume: status=%s err=%s", r1.Status, r1.Error)
	}
	if r1.Agent != agent.ExecAgentKey || r1.ResumeAgent != "fake-cli" {
		t.Fatalf("resume carrier agent=%q resume_agent=%q", r1.Agent, r1.ResumeAgent)
	}
	if got := readJobStdout(t, root, r1.ID); got != wantFirst {
		t.Fatalf("resume stdout = %q, want the same projected answer %q", got, wantFirst)
	}
	if r1.NDJSONKept == 0 || r1.NDJSONDropped == 0 {
		t.Fatalf("resume ndjson kept/dropped = %d/%d, want the projector to have run", r1.NDJSONKept, r1.NDJSONDropped)
	}
	stderr, err := store.NewFileStore(filepath.Join(root, "self")).ReadLogTail(r1.ID, store.StreamStderr, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stderr), "message_update") {
		t.Fatalf("resume stderr.log kept a dropped event type:\n%s", stderr)
	}

	// The stored request carries only the reference: no inherited env VALUES.
	for _, secret := range []string{"job-secret-value", "file-secret-value", "from-source-job", "/agent/home", "model-a"} {
		if strings.Contains(r1.RequestJSON, secret) {
			t.Fatalf("resume request_json leaks inherited value %q: %s", secret, r1.RequestJSON)
		}
	}
	if !strings.Contains(r1.RequestJSON, `"env_files":["creds/app.env"]`) || !strings.Contains(r1.RequestJSON, `"resumed_from":"`+first.ID+`"`) {
		t.Fatalf("resume request_json should reference env_files and the source job: %s", r1.RequestJSON)
	}

	// Resume of the resume still inherits (the chain is walked), and an explicit env
	// on the resume wins over everything inherited — and is the only env stored.
	r2, err := s.ResumeJobWith(r1.ID, "third", "", "caller", ResumeOptions{Env: map[string]string{"FAKE_OVR": "explicit", "FAKE_MODEL": "model-b"}})
	if err != nil {
		t.Fatal(err)
	}
	r2, _ = s.Wait(r2.ID)
	want2 := "final:FAKE_HOME=/agent/home;FAKE_MODEL=model-b;FAKE_JOB=job-secret-value;FAKE_FILE=file-secret-value;FAKE_OVR=explicit\n"
	if got := readJobStdout(t, root, r2.ID); got != want2 {
		t.Fatalf("resume-of-resume stdout = %q, want %q", got, want2)
	}
	for _, secret := range []string{"job-secret-value", "file-secret-value", "from-source-job", "/agent/home"} {
		if strings.Contains(r2.RequestJSON, secret) {
			t.Fatalf("second resume request_json leaks inherited value %q: %s", secret, r2.RequestJSON)
		}
	}
	if !strings.Contains(r2.RequestJSON, "explicit") {
		t.Fatalf("explicit resume env should be recorded like any submit env: %s", r2.RequestJSON)
	}
}

// A submit that merely NAMES resumed_from (no internal marker) inherits no env: the
// inheritance is a property of the resume entrypoint, not a way to read another
// job's env.
func TestPlainSubmitNamingResumedFromInheritsNoEnv(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newFakeNDJSONResumeService(t, root)
	first := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "fake-cli", Runner: "local", Prompt: "first", Cwd: ".", TimeoutSec: 30,
		Env: map[string]string{"FAKE_JOB": "job-secret-value"},
	})
	second := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "fake-cli", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30,
		ResumedFrom: first.ID,
	})
	if got := readJobStdout(t, root, second.ID); strings.Contains(got, "job-secret-value") {
		t.Fatalf("plain submit inherited another job's env: %q", got)
	}
}

// The worker re-enters Submit with the dispatch's carrier shape (Agent=exec,
// ResumeSourceAgent, ResumedFrom, an argv) and does NOT have the source job: it must
// still apply the source agent's env and output projection from ITS OWN config, and
// simply inherit no job-level env (job env never travels in a dispatch).
func TestWorkerStyleResumeCarrierUsesSourceAgentConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := newFakeNDJSONResumeService(t, root)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: agent.ExecAgentKey, Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd:               []string{testcmd.Path(t), "fake-ndjson-agent", "FAKE_HOME,FAKE_MODEL,FAKE_JOB"},
		ResumeSourceAgent: "fake-cli", ResumedFrom: "not-on-this-machine", SessionID: "fake-sess-1",
	})
	if final.Status != StatusDone {
		t.Fatalf("status=%s err=%s", final.Status, final.Error)
	}
	if got, want := readJobStdout(t, root, final.ID), "final:FAKE_HOME=/agent/home;FAKE_MODEL=model-a;FAKE_JOB=\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}
