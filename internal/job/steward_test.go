package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

func stewardSession(t *testing.T, s *Service, steward bool) JobResult {
	t.Helper()
	created, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "local", Cwd: ".",
		Prompt: "hello steward", Session: true, TimeoutSec: 20, IdleTimeoutSec: 20,
		Tags: []string{StewardTag}, Steward: steward,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSessionTurn(t, s, created.ID, 1)
	return created
}

func envLine(log, name string) string {
	for _, line := range strings.Split(log, "\n") {
		if v, ok := strings.CutPrefix(line, "acptest: env "+name+"="); ok {
			return v
		}
	}
	return ""
}

// The steward job carries the steward credential kind, the steward marker env and an
// injected gofer MCP server; an ordinary session job gets none of the three.
func TestStewardJobGetsStewardCredentialAndMCP(t *testing.T) {
	s := newACPService(t, t.TempDir(), acptest.Options{EnvPrint: []string{EnvJobToken, "GOFER_STEWARD", "GOFER_JOB_ID"}})

	st := stewardSession(t, s, true)
	log := readJobLog(t, st, store.StderrFile)
	if !strings.Contains(log, "acptest: session/new") || !strings.Contains(log, "mcp_servers=1") {
		t.Fatalf("steward session must advertise exactly the injected gofer MCP: %s", log)
	}
	if envLine(log, "GOFER_STEWARD") != "1" {
		t.Fatalf("steward marker env missing: %s", log)
	}
	tok := envLine(log, EnvJobToken)
	if !strings.HasPrefix(tok, JobTokenPrefix) {
		t.Fatalf("no job credential in the steward env: %q", tok)
	}
	look, ok := s.LookupJobToken(tok)
	if !ok || look.Kind != jobstore.JobCredentialSteward || look.JobID != st.ID {
		t.Fatalf("credential lookup = %+v ok=%v, want a steward credential of %s", look, ok, st.ID)
	}
	if !s.meta.IsStewardJob(st.ID) {
		t.Fatal("the steward marker must be durable (steward_jobs) so recovery can restore it")
	}
	_ = s.EndSession(st.ID)
	s.Wait(st.ID)

	plain := stewardSession(t, s, false)
	plog := readJobLog(t, plain, store.StderrFile)
	if !strings.Contains(plog, "mcp_servers=0") || envLine(plog, "GOFER_STEWARD") != "" {
		t.Fatalf("an ordinary session job must get neither MCP nor the steward marker: %s", plog)
	}
	pl, ok := s.LookupJobToken(envLine(plog, EnvJobToken))
	if !ok || pl.Kind != jobstore.JobCredentialMember {
		t.Fatalf("ordinary session credential = %+v ok=%v, want member", pl, ok)
	}
	if s.meta.IsStewardJob(plain.ID) {
		t.Fatal("an ordinary job must not be marked as the steward")
	}
	_ = s.EndSession(plain.ID)
	s.Wait(plain.ID)
}

// The marker is server-set: a JSON body can never carry it.
func TestStewardMarkerIsNotOnTheWire(t *testing.T) {
	var req JobRequest
	if err := json.Unmarshal([]byte(`{"project_key":"p","agent":"a","steward":true}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Steward {
		t.Fatal("a request body must not be able to set the steward marker")
	}
}

// A steward session recovered after a restart keeps its credential kind and MCP: the
// marker is read back from its durable row, not from request_json.
func TestRecoveredStewardSessionKeepsCredentialAndMCP(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{EnvPrint: []string{EnvJobToken, "GOFER_STEWARD"}})
	seedRecoverableSession(t, s, root, "recover-steward", StatusAwaitingInput)
	if err := s.meta.MarkStewardJob("recover-steward"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileOrphanJobs(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := s.Get("recover-steward")
		if s.entry("recover-steward") != nil && current.Status == StatusAwaitingInput {
			log := readJobLog(t, current, store.StderrFile)
			if !strings.Contains(log, "session/load") || !strings.Contains(log, "mcp_servers=1") {
				t.Fatalf("recovery did not reload the session: %s", log)
			}
			if envLine(log, "GOFER_STEWARD") != "1" {
				t.Fatalf("recovered steward lost its marker env: %s", log)
			}
			look, ok := s.LookupJobToken(envLine(log, EnvJobToken))
			if !ok || look.Kind != jobstore.JobCredentialSteward {
				t.Fatalf("recovered credential = %+v ok=%v, want steward", look, ok)
			}
			_ = s.EndSession(current.ID)
			s.Wait(current.ID)
			return
		}
		if IsTerminal(current.Status) {
			t.Fatalf("recovery reached %s: %s", current.Status, current.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("steward session was not reloaded")
}
