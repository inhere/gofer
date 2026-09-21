package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// connectWakeup wires a session over the shared test core and returns a finished
// job to register wakeups on (an agent can only register against a job that exists,
// and the common case is its own).
func connectWakeup(t *testing.T) (*mcp.ClientSession, *job.Service, job.JobResult) {
	t.Helper()
	session, jobs := connect(t)
	created, err := jobs.Submit(job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	final, ok := jobs.Wait(created.ID)
	if !ok {
		t.Fatalf("job %s never finished", created.ID)
	}
	return session, jobs, final
}

// TestWakeupTools (JOB-09): gofer_wakeup_create registers an event subscription that
// the JOB-09 matcher really reads, gofer_wakeup_list reports it, and
// gofer_wakeup_disable stops it — the three tools an agent uses to end its run and
// be resumed later instead of polling.
func TestWakeupTools(t *testing.T) {
	session, jobs, target := connectWakeup(t)
	ctx := context.Background()

	// Create: an event subscription on the job itself (no filter_job_id), which is
	// what "wake me when my own run is done" means.
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_wakeup_create",
		Arguments: map[string]any{
			"job_id":      target.ID,
			"kind":        "event",
			"event_types": []string{job.EventJobTerminal},
			"instruction": "merge the results",
		},
	})
	if err != nil {
		t.Fatalf("CallTool wakeup_create: %v", err)
	}
	var created wakeupView
	structured(t, res, &created)
	if created.ID == "" || created.JobID != target.ID || created.Kind != "event" {
		t.Fatalf("wakeup_create output = %+v, want an event wakeup on %s", created, target.ID)
	}
	if !created.Enabled || created.Mode != jobstore.WakeupModeOnce {
		t.Fatalf("wakeup_create output = %+v, want enabled and once by default", created)
	}
	if created.FilterJobID != target.ID {
		t.Fatalf("filter_job_id = %q, want the registering job", created.FilterJobID)
	}
	if len(created.EventTypes) != 1 || created.EventTypes[0] != job.EventJobTerminal {
		t.Fatalf("event_types = %v, want the subscribed type", created.EventTypes)
	}
	// The registration is real: the matcher's own query finds it.
	matched, err := jobs.Meta().MatchingEventWakeups(target.ID, job.EventJobTerminal)
	if err != nil {
		t.Fatalf("MatchingEventWakeups: %v", err)
	}
	if len(matched) != 1 || matched[0].ID != created.ID {
		t.Fatalf("matcher query = %+v, want the created wakeup", matched)
	}

	// A timer shape too, so the shared input struct is exercised for kind=every.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_wakeup_create",
		Arguments: map[string]any{
			"job_id": target.ID, "kind": "every", "every_sec": 3600, "instruction": "poll",
		},
	})
	if err != nil {
		t.Fatalf("CallTool wakeup_create (every): %v", err)
	}
	var every wakeupView
	structured(t, res, &every)
	if every.EverySec != 3600 || every.NextRunAt == 0 || every.Mode != jobstore.WakeupModeContinuous {
		t.Fatalf("every wakeup = %+v, want an armed continuous timer", every)
	}

	// List returns both.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_wakeup_list", Arguments: map[string]any{"job_id": target.ID},
	})
	if err != nil {
		t.Fatalf("CallTool wakeup_list: %v", err)
	}
	var list wakeupsView
	structured(t, res, &list)
	if len(list.Wakeups) != 2 {
		t.Fatalf("wakeup_list = %+v, want both wakeups", list.Wakeups)
	}

	// Disable stops it: the row is off and the matcher no longer returns it.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name: "gofer_wakeup_disable", Arguments: map[string]any{"wakeup_id": created.ID},
	})
	if err != nil {
		t.Fatalf("CallTool wakeup_disable: %v", err)
	}
	var off wakeupView
	structured(t, res, &off)
	if off.Enabled || off.ID != created.ID {
		t.Fatalf("wakeup_disable output = %+v, want the wakeup switched off", off)
	}
	matched, err = jobs.Meta().MatchingEventWakeups(target.ID, job.EventJobTerminal)
	if err != nil {
		t.Fatalf("MatchingEventWakeups: %v", err)
	}
	if len(matched) != 0 {
		t.Fatalf("matcher query = %+v, want a disabled wakeup to be invisible", matched)
	}

	// The derived input schema names the fields an agent fills in.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "gofer_wakeup_create" {
			continue
		}
		raw, merr := json.Marshal(tool.InputSchema)
		if merr != nil {
			t.Fatalf("marshal schema: %v", merr)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if uerr := json.Unmarshal(raw, &schema); uerr != nil {
			t.Fatalf("unmarshal schema: %v", uerr)
		}
		for _, want := range []string{"job_id", "kind", "every_sec", "event_types", "instruction"} {
			if _, ok := schema.Properties[want]; !ok {
				t.Fatalf("gofer_wakeup_create schema missing %q: %v", want, schema.Properties)
			}
		}
		return
	}
	t.Fatal("gofer_wakeup_create tool not registered")
}

// TestWakeupCreateToolRefusesBadSpec: a validation failure reaches the caller as an
// MCP error result (the SDK's IsError), not as a silent success.
func TestWakeupCreateToolRefusesBadSpec(t *testing.T) {
	session, _, target := connectWakeup(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_wakeup_create",
		Arguments: map[string]any{
			"job_id": target.ID, "kind": "event", "event_types": []string{"job.exploded"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("an unknown event type was accepted: %+v", res.Content)
	}

	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gofer_wakeup_create",
		Arguments: map[string]any{
			"job_id": "no-such-job", "kind": "at", "at": time.Now().Add(time.Hour).Unix(),
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatalf("an unknown target job was accepted: %+v", res.Content)
	}
}
