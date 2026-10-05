package job

import (
	"errors"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	acprunner "github.com/inhere/gofer/internal/runner/acp"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newResumeModeService hosts, in one project, a cli agent "claude" (dual-mode: batch +
// interactive, so it carries both resume templates), a fake-server acp agent
// "claude-acp" (same session family as claude) and an unrelated cli agent "codex2"
// (own family, interactive-capable). The interactivity switch of the project is
// parameterised so the allow_interactive check can be exercised.
func newResumeModeService(t *testing.T, root string, allowInteractive bool) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:         root,
				AllowedAgents:    []string{"claude", "claude-acp", "codex2", "exec"},
				AllowedRunners:   []string{"local"},
				AllowInteractive: boolPtr(allowInteractive),
				AllowExec:        true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"claude":     {Type: agent.TypeCLIAgent, Command: "claude", Args: []string{"-p", "{{prompt}}"}, InteractiveArgs: []string{}},
			"claude-acp": {Type: agent.TypeACPAgent, Command: testcmd.Path(t), Args: acptest.CmdArgs(acptest.Options{})},
			"codex2":     {Type: agent.TypeCLIAgent, Command: "codex2", Args: []string{"{{prompt}}"}, SessionResume: []string{"resume", "{{session_id}}", "{{prompt}}"}},
		},
	}
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{
		localrunner.Name: localrunner.New(),
		acprunner.Name:   acprunner.New(),
	}
	meta, err := jobstore.Open(jobstoreDBPath(root))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })
	s := NewService(cfg, projReg, agentReg, runners, meta, nil)
	s.runners[builtinPtyRunner] = &recordingRunner{name: builtinPtyRunner}
	return drainOnClose(t, s)
}

func resumeModeACPSource(t *testing.T, s *Service) JobResult {
	t.Helper()
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude-acp", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 30,
	})
	if src.Status != StatusDone || src.SessionID == "" {
		t.Fatalf("ACP source status=%s session=%q err=%s", src.Status, src.SessionID, src.Error)
	}
	return src
}

func resumeModeCLISource(t *testing.T, s *Service, interactive bool) JobResult {
	t.Helper()
	src := submitSourceCancel(t, s, JobRequest{
		ProjectKey: "self", Agent: "claude", Runner: "local",
		Interactive: interactive, Prompt: "start", Cwd: ".", TimeoutSec: 30,
	})
	if src.SessionID == "" {
		t.Fatalf("setup: claude should have an injected session id")
	}
	return src
}

// TestResumeModeACPSourceAsCLI: an ACP session of the claude family can be continued
// by the claude CLI, both as a one-shot batch turn and as a pty job.
func TestResumeModeACPSourceAsCLI(t *testing.T) {
	t.Parallel()
	s := newResumeModeService(t, t.TempDir(), true)
	src := resumeModeACPSource(t, s)

	batch, err := s.ResumeJobWith(src.ID, "next", "", "caller", ResumeOptions{Mode: ResumeModeBatch, Agent: "claude"})
	if err != nil {
		t.Fatalf("batch resume: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(batch.ID); s.Wait(batch.ID) })
	if want := []string{"claude", "--resume", src.SessionID, "-p", "next"}; !equalArgs(resumeArgv(t, batch.RequestJSON), want) {
		t.Fatalf("batch argv = %#v, want %#v", resumeArgv(t, batch.RequestJSON), want)
	}
	if batch.Interactive || batch.SessionID != src.SessionID || batch.ResumedFrom != src.ID {
		t.Fatalf("batch job interactive=%v session=%q resumed_from=%q", batch.Interactive, batch.SessionID, batch.ResumedFrom)
	}

	pty, err := s.ResumeJobWith(src.ID, "", "", "caller", ResumeOptions{Mode: ResumeModeInteractive, Agent: "claude"})
	if err != nil {
		t.Fatalf("interactive resume: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(pty.ID); s.Wait(pty.ID) })
	got := resumeStoredRequest(t, pty.RequestJSON)
	if !pty.Interactive || !got.Interactive || !equalArgs(got.Cmd, []string{"claude", "--resume", src.SessionID}) {
		t.Fatalf("interactive job interactive=%v argv=%#v", pty.Interactive, got.Cmd)
	}
}

// TestResumeModeRejectsACPToCLIWithoutAgent: with no --agent the ACP agent itself is
// the target, and an ACP session cannot be resumed as a CLI job; an unrelated agent
// (another family) is refused too.
func TestResumeModeRejectsUnsupportedCombos(t *testing.T) {
	t.Parallel()
	s := newResumeModeService(t, t.TempDir(), true)
	src := resumeModeACPSource(t, s)

	for name, opts := range map[string]ResumeOptions{
		"acp as batch cli":       {Mode: ResumeModeBatch},
		"acp as interactive cli": {Mode: ResumeModeInteractive},
		"other family agent":     {Mode: ResumeModeBatch, Agent: "codex2"},
		"unknown agent":          {Mode: ResumeModeBatch, Agent: "nope"},
	} {
		if _, err := s.ResumeJobWith(src.ID, "x", "", "caller", opts); !errors.Is(err, ErrResumeUnsupported) {
			t.Errorf("%s: err = %v, want ErrResumeUnsupported", name, err)
		}
	}
	if _, err := s.ResumeJobWith(src.ID, "x", "", "caller", ResumeOptions{Mode: "turbo"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("unknown mode: err = %v, want ErrInvalidRequest", err)
	}
}

// TestResumeModeCLISourceAsSessionAndPty: a claude CLI session continues as a resident
// ACP session on its family's ACP agent, and a pty source can continue as a batch job.
func TestResumeModeCLISourceAsSessionAndPty(t *testing.T) {
	t.Parallel()
	s := newResumeModeService(t, t.TempDir(), true)

	cli := resumeModeCLISource(t, s, false)
	// --mode session without an ACP target is refused: claude is not an acp-agent.
	if _, err := s.ResumeJobWith(cli.ID, "", "", "caller", ResumeOptions{Mode: ResumeModeSession}); !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("session on cli agent err = %v, want ErrResumeUnsupported", err)
	}
	sess, err := s.ResumeJobWith(cli.ID, "", "", "caller", ResumeOptions{Mode: ResumeModeSession, Agent: "claude-acp"})
	if err != nil {
		t.Fatalf("session resume: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(sess.ID); s.Wait(sess.ID) })
	if sess.Agent != "claude-acp" || !sess.Session || sess.SessionID != cli.SessionID || sess.ResumedFrom != cli.ID {
		t.Fatalf("session job agent=%q session=%v sid=%q from=%q", sess.Agent, sess.Session, sess.SessionID, sess.ResumedFrom)
	}

	// cli batch -> pty
	pty, err := s.ResumeJobWith(cli.ID, "", "", "caller", ResumeOptions{Mode: ResumeModeInteractive})
	if err != nil {
		t.Fatalf("batch->interactive: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(pty.ID); s.Wait(pty.ID) })
	if !pty.Interactive {
		t.Fatal("batch->interactive job is not interactive")
	}

	// pty -> batch needs a prompt, then runs `--resume -p`.
	ptySrc := resumeModeCLISource(t, s, true)
	if _, err := s.ResumeJobWith(ptySrc.ID, " ", "", "caller", ResumeOptions{Mode: ResumeModeBatch}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("pty->batch empty prompt err = %v, want ErrInvalidRequest", err)
	}
	batch, err := s.ResumeJobWith(ptySrc.ID, "go", "", "caller", ResumeOptions{Mode: ResumeModeBatch})
	if err != nil {
		t.Fatalf("pty->batch: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(batch.ID); s.Wait(batch.ID) })
	if batch.Interactive || !equalArgs(resumeArgv(t, batch.RequestJSON), []string{"claude", "--resume", ptySrc.SessionID, "-p", "go"}) {
		t.Fatalf("pty->batch interactive=%v argv=%#v", batch.Interactive, resumeArgv(t, batch.RequestJSON))
	}
}

// TestResumeModeInteractiveNeedsProjectSwitch: asking for a pty continuation in a
// project that disallows interactive jobs is a clear 400, before anything is submitted.
func TestResumeModeInteractiveNeedsProjectSwitch(t *testing.T) {
	t.Parallel()
	s := newResumeModeService(t, t.TempDir(), false)
	src := resumeModeACPSource(t, s)
	_, err := s.ResumeJobWith(src.ID, "", "", "caller", ResumeOptions{Mode: ResumeModeInteractive, Agent: "claude"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest (allow_interactive)", err)
	}
}

// TestResumeLegacyTTYAgentJob: a job recorded before the built-in tty-claude template
// was removed still names it. It stays viewable, and resuming it maps to the dual-mode
// claude and keeps the pty form (the source was interactive); an unknown tty-* name
// that has no replacement is still a plain ErrResumeUnsupported.
func TestResumeLegacyTTYAgentJob(t *testing.T) {
	t.Parallel()
	s := newResumeModeService(t, t.TempDir(), true)
	src := resumeModeCLISource(t, s, true)

	// The source is long finished, so it lives in the metadata store: rewrite its
	// recorded agent there, as an old database would have it.
	rewrite := func(id, key string) {
		rec, ok, err := s.meta.GetJob(id)
		if err != nil || !ok {
			t.Fatalf("job %s not in the store: ok=%v err=%v", id, ok, err)
		}
		rec.Agent = key
		if err := s.meta.UpsertJob(rec); err != nil {
			t.Fatalf("rewrite agent: %v", err)
		}
		s.mu.Lock()
		delete(s.jobs, id)
		s.mu.Unlock()
	}
	rewrite(src.ID, "tty-claude")
	if got, ok := s.Get(src.ID); !ok || got.Agent != "tty-claude" {
		t.Fatalf("legacy job not viewable as recorded: ok=%v agent=%q", ok, got.Agent)
	}

	pty, err := s.ResumeJob(src.ID, "", "", "caller")
	if err != nil {
		t.Fatalf("resume legacy tty-claude job: %v", err)
	}
	t.Cleanup(func() { _ = s.Cancel(pty.ID); s.Wait(pty.ID) })
	got := resumeStoredRequest(t, pty.RequestJSON)
	// The continuation runs through the exec carrier; what matters is the claude argv.
	if !pty.Interactive || !equalArgs(got.Cmd, []string{"claude", "--resume", src.SessionID}) {
		t.Fatalf("resumed interactive=%v argv=%#v, want claude pty resume", pty.Interactive, got.Cmd)
	}

	rewrite(src.ID, "tty-gemini")
	if _, err := s.ResumeJob(src.ID, "", "", "caller"); !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("unmapped tty-* agent err = %v, want ErrResumeUnsupported", err)
	}
}
