package commands

import (
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
)

func bindToolBgAgent(t *testing.T, sub string) *gcli.Command {
	t.Helper()
	for _, def := range NewToolBgAgentCmd().Subs {
		if def.Name == sub {
			c := gcli.NewCommand(def.Name, def.Desc, nil)
			def.Config(c)
			return c
		}
	}
	t.Fatalf("no tool bg-agent subcommand %q", sub)
	return nil
}

const bgAgentViewJSON = `{"version":1,"kinds":[
{"kind":"summarizer","sessions":[{"id":"s-1","agent":"a1","project":"demo","runs":3,"created_at":1,"last_used_at":2,"context_used_tokens":30,"context_limit_tokens":100,"context_ratio":0.3,"last_usage":null}],
 "today":{"runs":2,"blocked":0,"input_tokens":10,"output_tokens":5,"total_tokens":15,"cost_usd":0.01,"usage_runs":2},"policy":{"max_runs":20,"max_context_ratio":0.7,"max_age_sec":0}},
{"kind":"steward","sessions":[{"id":"s-2","runs":1,"created_at":1,"last_used_at":2,"context_used_tokens":null,"context_limit_tokens":null,"context_ratio":null,"last_usage":null}],
 "today":{"runs":0,"blocked":0,"input_tokens":0,"output_tokens":0,"total_tokens":0,"cost_usd":0,"usage_runs":0},"policy":{"max_runs":20,"max_context_ratio":0.7,"max_age_sec":0}},
{"kind":"job_check","sessions":[],"today":{"runs":0,"blocked":0,"input_tokens":0,"output_tokens":0,"total_tokens":0,"cost_usd":0,"usage_runs":0},"policy":{"max_runs":20,"max_context_ratio":0.7,"max_age_sec":0}}]}`

func TestToolBgAgentLsPrintsSessionsAndUnknownContext(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	stub := newTriggerStub(t, map[string]string{"GET /v1/background-agents": bgAgentViewJSON})
	out := captureOutput(t, func() {
		if err := runToolBgAgentLs(bindToolBgAgent(t, "ls"), nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	for _, want := range []string{"KIND", "summarizer", "steward", "job_check", "session s-1", "context: 30%", "session s-2", "context: unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "context: 0%") {
		t.Errorf("an unknown context must not print as 0%%:\n%s", out)
	}
	if han.MatchString(out) {
		t.Errorf("English output carries CJK:\n%s", out)
	}
	if got := stub.last(t); got.Method != "GET" || got.Header.Get("Authorization") != "Bearer tkn-operator" {
		t.Errorf("request = %+v", got)
	}
}

func TestToolBgAgentLsJSONAndZh(t *testing.T) {
	t.Setenv("GOFER_LANG", "zh-CN")
	newTriggerStub(t, map[string]string{"GET /v1/background-agents": bgAgentViewJSON})
	cmd := bindToolBgAgent(t, "ls")
	toolBgAgentLsOpts.jsonOut = true
	t.Cleanup(func() { toolBgAgentLsOpts.jsonOut = false })
	out := captureOutput(t, func() {
		if err := runToolBgAgentLs(cmd, nil); err != nil {
			t.Fatalf("ls --json: %v", err)
		}
	})
	if !strings.Contains(out, `"version": 1`) || !strings.Contains(out, `"context_ratio": null`) {
		t.Errorf("json output:\n%s", out)
	}
	toolBgAgentLsOpts.jsonOut = false
	out = captureOutput(t, func() {
		if err := runToolBgAgentLs(bindToolBgAgent(t, "ls"), nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	if !strings.Contains(out, "上下文：未知") {
		t.Errorf("zh-CN output lacks the unknown context:\n%s", out)
	}
}

func TestToolBgAgentReset(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	stub := newTriggerStub(t, map[string]string{
		"POST /v1/background-agents/steward/session/reset": `{"version":1,"reset":2,"kind":{"kind":"steward","sessions":[],"today":{},"policy":{}}}`,
	})
	out := captureOutput(t, func() {
		if err := runToolBgAgentReset(bindToolBgAgent(t, "reset"), []string{"steward"}); err != nil {
			t.Fatalf("reset: %v", err)
		}
	})
	if !strings.Contains(out, "steward: 2 session(s) reset") {
		t.Errorf("reset output:\n%s", out)
	}
	if got := stub.last(t); got.Method != "POST" || got.Path != "/v1/background-agents/steward/session/reset" {
		t.Errorf("request = %+v", got)
	}
	if err := runToolBgAgentReset(bindToolBgAgent(t, "reset"), []string{""}); err == nil {
		t.Error("an empty kind must be refused")
	}
}

func TestToolBgAgentResetRefusedByServer(t *testing.T) {
	t.Setenv("GOFER_LANG", "en")
	stub := newTriggerStub(t, map[string]string{"POST /v1/background-agents/nope/session/reset": `{"error":"unknown background agent"}`})
	stub.status["POST /v1/background-agents/nope/session/reset"] = 404
	if err := runToolBgAgentReset(bindToolBgAgent(t, "reset"), []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown background agent") {
		t.Fatalf("err = %v, want the server's refusal", err)
	}
}
