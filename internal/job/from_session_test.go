package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestCheckFromSession(t *testing.T) {
	for _, ok := range []string{"", "0d7c1f8e-1a2b-4c3d-9e8f-001122334455", "sess_abc"} {
		if err := CheckFromSession(ok); err != nil {
			t.Fatalf("CheckFromSession(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"-x", "--from", "a b", "a\nb", strings.Repeat("s", 201)} {
		if err := CheckFromSession(bad); err == nil {
			t.Fatalf("CheckFromSession(%q) = nil, want an error", bad)
		}
	}
}

// newFromSessionService: two suag-family agents with from_session_args, one agent of
// another family with it, and one cli-agent without it.
func newFromSessionService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	suag := func(family string) config.AgentConfig {
		return config.AgentConfig{
			Type: agent.TypeCLIAgent, Command: "suag",
			Args:            []string{"run", "--prompt", "{{prompt}}"},
			SessionInject:   []string{"--session-id", "{{session_id}}"},
			FromSessionArgs: []string{"--from", "{{from_session}}"},
			SessionFamily:   family,
		}
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"self": {
			HostPath: root, AllowedRunners: []string{"local"}, AllowExec: true,
		}},
		Agents: map[string]config.AgentConfig{
			"suag":  suag("suag"),
			"suag2": suag("suag"),
			"other": suag("other"),
			"plain": {Type: agent.TypeCLIAgent, Command: "plain-tool", Args: []string{"{{prompt}}"}},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

func TestSubmitFromSessionAdmission(t *testing.T) {
	t.Parallel()
	s := newFromSessionService(t)
	base := JobRequest{ProjectKey: "self", Runner: "local", Prompt: "x", FromSession: "s-old"}
	with := func(f func(*JobRequest)) JobRequest { r := base; f(&r); return r }
	for name, req := range map[string]JobRequest{
		"agent without from_session_args": with(func(r *JobRequest) { r.Agent = "plain" }),
		"exec agent":                      with(func(r *JobRequest) { r.Agent = "exec"; r.Prompt = ""; r.Cmd = []string{"true"} }),
		"flag-like id":                    with(func(r *JobRequest) { r.Agent = "suag"; r.FromSession = "--evil" }),
		"with session_id":                 with(func(r *JobRequest) { r.Agent = "suag"; r.SessionID = "s-new" }),
		"with resumed_from":               with(func(r *JobRequest) { r.Agent = "suag"; r.ResumedFrom = "job-1" }),
	} {
		if _, err := s.Submit(req); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

// TestSubmitFromSessionRendersAndRecords: the fragment is appended to the argv next to
// the injected NEW session id, and from_session lands in request_json / JobResult (live
// and read back from the record); a job without it renders no --from.
func TestSubmitFromSessionRendersAndRecords(t *testing.T) {
	t.Parallel()
	s := newFromSessionService(t)
	res := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "suag", Runner: "local", Prompt: "go on", Cwd: ".", TimeoutSec: 30, FromSession: " s-old "})
	if res.FromSession != "s-old" || templateRequest(t, res).FromSession != "s-old" {
		t.Fatalf("from_session not recorded: result=%q request=%q", res.FromSession, templateRequest(t, res).FromSession)
	}
	if !strings.Contains(res.RenderedCommand, `"--prompt","go on","--from","s-old","--session-id"`) {
		t.Fatalf("rendered command %q lacks --from s-old before the injected session id", res.RenderedCommand)
	}
	if res.SessionID == "" || res.SessionID == "s-old" {
		t.Fatalf("session id = %q, want a NEW injected id", res.SessionID)
	}
	if got := fromRecord(jobstore.JobRecord{RequestJSON: res.RequestJSON}).FromSession; got != "s-old" {
		t.Fatalf("fromRecord FromSession = %q", got)
	}
	plain := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "suag", Runner: "local", Prompt: "go on", Cwd: ".", TimeoutSec: 30})
	if plain.FromSession != "" || strings.Contains(plain.RequestJSON, "from_session") || strings.Contains(plain.RenderedCommand, "--from") {
		t.Fatalf("job without from_session changed: %q / %s / %q", plain.FromSession, plain.RequestJSON, plain.RenderedCommand)
	}
}

// TestSubmitFromSessionFamily: when gofer knows which agent started the source session
// (a job bound to it), a different agent must share its session family; an unknown id
// passes through (the agent CLI reports a missing session itself).
func TestSubmitFromSessionFamily(t *testing.T) {
	t.Parallel()
	s := newFromSessionService(t)
	src := submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "suag", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if src.SessionID == "" {
		t.Fatal("source job has no session id")
	}
	if _, err := s.Submit(JobRequest{ProjectKey: "self", Agent: "other", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30, FromSession: src.SessionID}); !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "session family") {
		t.Fatalf("cross-family err = %v, want a session family refusal", err)
	}
	for _, key := range []string{"suag", "suag2"} {
		submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: key, Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30, FromSession: src.SessionID})
	}
	submitSourceCancel(t, s, JobRequest{ProjectKey: "self", Agent: "other", Runner: "local", Prompt: "x", Cwd: ".", TimeoutSec: 30, FromSession: "never-seen"})
}
