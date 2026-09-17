package job

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/store"
)

// readJobLog reads one of a job's captured log files (S2 tests assert on what the
// AGENT saw — the fake ACP server writes the protocol calls it served to stderr).
func readJobLog(t *testing.T, jr JobResult, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(jr.ResultDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestACPResumeUsesSessionLoad: an acp-agent source job resumes as a NEW acp-agent
// job (not the exec carrier) that LOADS the source session over the protocol —
// initialize → session/load{sessionId} → session/prompt — and never opens a fresh
// session. The fake server prints every session call it serves to stderr, so the
// assertion is on what the agent was actually asked to do.
func TestACPResumeUsesSessionLoad(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})

	src := acpSubmit(t, s, 30)
	if src.Status != StatusDone {
		t.Fatalf("source status = %s (err=%s), want done", src.Status, src.Error)
	}
	if src.SessionID != acptest.SessionID {
		t.Fatalf("source session_id = %q, want %q", src.SessionID, acptest.SessionID)
	}

	resumed, err := s.ResumeJob(src.ID, "keep going", "", "caller-acp")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if resumed.Agent != "acpbot" {
		t.Fatalf("resumed agent = %q, want the source's acp-agent key %q", resumed.Agent, "acpbot")
	}
	if resumed.ResumedFrom != src.ID || resumed.SourceJobID != src.ID {
		t.Fatalf("resumed lineage = %q/%q, want %s", resumed.ResumedFrom, resumed.SourceJobID, src.ID)
	}
	if resumed.SessionID != src.SessionID {
		t.Fatalf("resumed session_id = %q, want the source's %q", resumed.SessionID, src.SessionID)
	}

	final, ok := s.Wait(resumed.ID)
	if !ok {
		t.Fatalf("resumed job %s not found", resumed.ID)
	}
	if final.Status != StatusDone {
		t.Fatalf("resumed status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.SessionID != src.SessionID {
		t.Fatalf("resumed final session_id = %q, want %q", final.SessionID, src.SessionID)
	}
	// The continuation ran a real turn: the agent's text landed on stdout.
	if out := readJobLog(t, final, store.StdoutFile); !strings.Contains(out, acptest.TextDone) {
		t.Fatalf("resumed stdout = %q, want the agent's turn text", out)
	}

	agentLog := readJobLog(t, final, store.StderrFile)
	if !strings.Contains(agentLog, "session/load sid="+src.SessionID) {
		t.Fatalf("the agent was not asked to load session %s:\n%s", src.SessionID, agentLog)
	}
	if strings.Contains(agentLog, "session/new") {
		t.Fatalf("a resume must not open a new session:\n%s", agentLog)
	}
}

// TestACPResumeRefusedWhenLoadSessionDisabled: an agent whose config declares
// acp.load_session: false is refused UP FRONT (ErrResumeUnsupported → 400) — the
// declaration means "don't even try", so no job is submitted for the agent to reject
// over the protocol. The automatic continuation makes the same call (resumable).
func TestACPResumeRefusedWhenLoadSessionDisabled(t *testing.T) {
	root := t.TempDir()
	noLoad := false
	s := newACPServiceAgent(t, root, acptest.Options{}, nil, func(ac *config.AgentConfig) {
		ac.ACP = &config.ACPConfig{LoadSession: &noLoad}
	})

	src := acpSubmit(t, s, 30)
	if src.Status != StatusDone || src.SessionID == "" {
		t.Fatalf("setup: source = %s/%q, want done with a session", src.Status, src.SessionID)
	}

	if _, err := s.ResumeJob(src.ID, "keep going", "", "caller-acp"); !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("ResumeJob with acp.load_session=false err = %v, want ErrResumeUnsupported", err)
	}
	ac, ok := s.agents.Get("acpbot")
	if !ok || resumable(ac) {
		t.Fatalf("resumable(acpbot with load_session=false) = %v, want false", ok)
	}
}

// TestACPResumeFailsWhenLoadUnsupported: the agent reports
// agentCapabilities.loadSession=false (and refuses session/load) — the resume must
// FAIL with an explicit error instead of silently continuing in a fresh session
// with no context.
func TestACPResumeFailsWhenLoadUnsupported(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{RefuseLoad: true})

	src := acpSubmit(t, s, 30)
	if src.Status != StatusDone || src.SessionID != acptest.SessionID {
		t.Fatalf("setup: source = %s/%q, want done/%q", src.Status, src.SessionID, acptest.SessionID)
	}

	resumed, err := s.ResumeJob(src.ID, "keep going", "", "caller-acp")
	if err != nil {
		t.Fatalf("ResumeJob must submit (the agent's refusal is discovered over the protocol): %v", err)
	}
	final, ok := s.Wait(resumed.ID)
	if !ok {
		t.Fatalf("resumed job %s not found", resumed.ID)
	}
	if final.Status != StatusFailed {
		t.Fatalf("resumed status = %s, want failed (no silent fallback to session/new)", final.Status)
	}
	if !strings.Contains(final.Error, "session/load") {
		t.Fatalf("resumed error = %q, want it to name session/load", final.Error)
	}
	agentLog := readJobLog(t, final, store.StderrFile)
	if !strings.Contains(agentLog, "session/load") {
		t.Fatalf("the job's stderr must record the load attempt:\n%s", agentLog)
	}
	if strings.Contains(agentLog, "session/new") {
		t.Fatalf("the failed resume must not fall back to a new session:\n%s", agentLog)
	}
}

// TestACPResumeInheritsTimeoutTagsTitleCwd: like the exec-carrier resume, an
// acp-agent continuation is governed like the run it continues — the source's
// timeout, tags and (resumed-marked) title, and the same cwd.
func TestACPResumeInheritsTimeoutTagsTitleCwd(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})

	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local",
		Prompt: acpTestPrompt, Cwd: ".", TimeoutSec: 2400,
		Tags: []string{"nightly", "wave2"}, Title: "big refactor",
	})
	if src.Status != StatusDone {
		t.Fatalf("setup: source status = %s (err=%s), want done", src.Status, src.Error)
	}
	if src.TimeoutSec != 2400 {
		t.Fatalf("setup: source timeout_sec = %d, want 2400", src.TimeoutSec)
	}

	resumed, err := s.ResumeJob(src.ID, "continue", "", "caller-inherit")
	if err != nil {
		t.Fatalf("ResumeJob: %v", err)
	}
	if _, ok := s.Wait(resumed.ID); !ok {
		t.Fatalf("resumed job %s not found", resumed.ID)
	}

	if resumed.TimeoutSec != 2400 {
		t.Fatalf("resumed timeout_sec = %d, want the source's 2400", resumed.TimeoutSec)
	}
	if got := strings.Join(resumed.Tags, ","); got != "nightly,wave2" {
		t.Fatalf("resumed tags = %q, want nightly,wave2", got)
	}
	if resumed.Title != "big refactor (resumed)" {
		t.Fatalf("resumed title = %q, want %q", resumed.Title, "big refactor (resumed)")
	}
	if resumed.Cwd != src.Cwd {
		t.Fatalf("resumed cwd = %q, want the source's %q", resumed.Cwd, src.Cwd)
	}
}

// TestAutoResumeACPAgent: automatic resume (v0.42) covers acp-agents too — the
// transient-failure continuation is dispatched with the source session, and it
// resumes THAT session over session/load.
func TestAutoResumeACPAgent(t *testing.T) {
	root := t.TempDir()
	const transient = "stream disconnected before completion"
	s := newACPServiceAgent(t, root, acptest.Options{PromptError: transient}, nil, func(ac *config.AgentConfig) {
		// The built-in transient table is keyed on the agent's command (here the test
		// binary), so the patterns are declared explicitly.
		ac.TransientErrorPatterns = []string{"stream disconnected"}
	})

	src := acpSubmit(t, s, 30)
	if src.Status != StatusFailed {
		t.Fatalf("source status = %s, want failed (the scripted prompt error)", src.Status)
	}
	if src.SessionID != acptest.SessionID {
		t.Fatalf("source session_id = %q, want %q", src.SessionID, acptest.SessionID)
	}
	src = waitAutoResumed(t, s, src.ID, true)

	cont, ok := s.Get(src.AutoResumedBy)
	if !ok {
		t.Fatalf("continuation %s not found", src.AutoResumedBy)
	}
	if cont.ResumedFrom != src.ID || cont.AutoResumeAttempt != 1 || cont.SourceJobID != src.ID {
		t.Fatalf("continuation lineage = %q/%d/%q, want %s/1/%s",
			cont.ResumedFrom, cont.AutoResumeAttempt, cont.SourceJobID, src.ID, src.ID)
	}
	if cont.SessionID != src.SessionID {
		t.Fatalf("continuation session_id = %q, want the source's %q", cont.SessionID, src.SessionID)
	}
	final, _ := s.Wait(cont.ID)
	agentLog := readJobLog(t, final, store.StderrFile)
	if !strings.Contains(agentLog, "session/load sid="+src.SessionID) {
		t.Fatalf("the automatic continuation must resume via session/load:\n%s", agentLog)
	}
	if strings.Contains(agentLog, "session/new") {
		t.Fatalf("the automatic continuation must not open a new session:\n%s", agentLog)
	}
}
