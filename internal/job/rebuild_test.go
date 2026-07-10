package job

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/secret"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func requestFromJSON(t *testing.T, raw string) JobRequest {
	t.Helper()
	var req JobRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("request_json not valid JSON: %v (%q)", err, raw)
	}
	return req
}

func TestRebuildJobEmptyOverridesStampsFreshFields(t *testing.T) {
	s := newTestService(t, t.TempDir())
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", Env: map[string]string{"KEEP": "one"},
		RequestID: "request-source", SessionID: "session-source", CallerID: "source-caller",
		PlanID: "plan-x", TimeoutSec: 30,
	})

	rebuilt, err := s.RebuildJob(src.ID, RebuildOverrides{}, "caller-new", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob: %v", err)
	}
	if rebuilt.SourceJobID != src.ID {
		t.Fatalf("source_job_id = %q, want %q", rebuilt.SourceJobID, src.ID)
	}
	if rebuilt.CallerID != "caller-new" || rebuilt.Client != "127.0.0.1" {
		t.Fatalf("caller/client = %q/%q, want caller-new/127.0.0.1", rebuilt.CallerID, rebuilt.Client)
	}
	req := requestFromJSON(t, rebuilt.RequestJSON)
	if req.RequestID != "" || req.SessionID != "" {
		t.Fatalf("request/session not cleared in rebuilt request: %q/%q", req.RequestID, req.SessionID)
	}
	if req.ProjectKey != "self" || req.Agent != "exec" || req.Runner != "local" || req.Cwd != "." {
		t.Fatalf("base fields not inherited: %+v", req)
	}
	if !reflect.DeepEqual(req.Cmd, []string{"go", "version"}) {
		t.Fatalf("cmd = %#v, want [go version]", req.Cmd)
	}
	if req.Env["KEEP"] != "one" {
		t.Fatalf("env KEEP = %q, want one", req.Env["KEEP"])
	}
	if req.PlanID != "plan-x" {
		t.Fatalf("plan_id = %q, want plan-x", req.PlanID)
	}
}

func TestRebuildJobEnvOverridesMergeAndUnsetWins(t *testing.T) {
	s := newTestService(t, t.TempDir())
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".",
		Env: map[string]string{"A": "1", "B": "2", "KEEP": "3"}, TimeoutSec: 30,
	})

	rebuilt, err := s.RebuildJob(src.ID, RebuildOverrides{
		EnvSet:   map[string]string{"A": "9", "C": "4"},
		EnvUnset: []string{"B"},
	}, "caller-new", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob env merge: %v", err)
	}
	req := requestFromJSON(t, rebuilt.RequestJSON)
	if req.Env["A"] != "9" || req.Env["C"] != "4" || req.Env["KEEP"] != "3" {
		t.Fatalf("env merge wrong: %#v", req.Env)
	}
	if _, ok := req.Env["B"]; ok {
		t.Fatalf("env_unset should remove B, got %#v", req.Env)
	}

	unsetWins, err := s.RebuildJob(src.ID, RebuildOverrides{
		EnvSet:   map[string]string{"A": "9"},
		EnvUnset: []string{"A"},
	}, "caller-new", "127.0.0.1")
	if err != nil {
		t.Fatalf("RebuildJob unset-wins: %v", err)
	}
	req = requestFromJSON(t, unsetWins.RequestJSON)
	if _, ok := req.Env["A"]; ok {
		t.Fatalf("env_unset must win when env_set/env_unset both mention A, got %#v", req.Env)
	}
	if req.Env["B"] != "2" || req.Env["KEEP"] != "3" {
		t.Fatalf("unmentioned env keys should be preserved, got %#v", req.Env)
	}
}

func TestRebuildJobRejectsPlaceholdersAndUnknownSource(t *testing.T) {
	s := newTestService(t, t.TempDir())
	src := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})

	prompt := "token=" + secret.Placeholder
	if _, err := s.RebuildJob(src.ID, RebuildOverrides{Prompt: &prompt}, "caller-new", "127.0.0.1"); !errors.Is(err, ErrRedactedPlaceholder) {
		t.Fatalf("placeholder prompt err = %v, want ErrRedactedPlaceholder", err)
	}

	agentArgs := []string{"--api-key=" + secret.Placeholder}
	if _, err := s.RebuildJob(src.ID, RebuildOverrides{AgentArgs: &agentArgs}, "caller-new", "127.0.0.1"); !errors.Is(err, ErrRedactedPlaceholder) {
		t.Fatalf("placeholder agent_args err = %v, want ErrRedactedPlaceholder", err)
	}

	if _, err := s.RebuildJob("missing", RebuildOverrides{}, "caller-new", "127.0.0.1"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("unknown source err = %v, want ErrUnknownJob", err)
	}
}

func TestRebuildJobRejectsRunningSource(t *testing.T) {
	s := newTestService(t, t.TempDir())
	src, err := s.Submit(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "sleep", "2s"), Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForStatus(t, s, src.ID, StatusRunning, 2*time.Second)
	t.Cleanup(func() {
		_ = s.Cancel(src.ID)
		s.Wait(src.ID)
	})

	_, err = s.RebuildJob(src.ID, RebuildOverrides{}, "caller-new", "127.0.0.1")
	if !errors.Is(err, ErrJobNotTerminal) {
		t.Fatalf("RebuildJob running source err = %v, want ErrJobNotTerminal", err)
	}
}
