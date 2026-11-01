package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestMemorySynthesisCommands(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.EN))
	var paths []string
	run := jobstore.MemorySynthesisRun{ID: "ms-1", ProjectKey: "p", Day: "2026-10-28", Status: "succeeded", InputDigest: "0123456789abcdef0123",
		MaterialSchema: "daily-memory-materials.v1", MaterialCountsJSON: `{"job_report":2}`, Attempt: 2, StartedAt: 1000, FinishedAt: 1010,
		LastCheckedAt: 1020, ResultJSON: `{"summary":"ok"}`, MaterialSnapshot: `{"secret":"never printed"}`}
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/memory-syntheses" {
			list := run
			list.ResultJSON, list.MaterialSnapshot = "", ""
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": []jobstore.MemorySynthesisRun{list}})
			return
		}
		_ = json.NewEncoder(w).Encode(run)
	})
	reset := func() { memorySynthesisOpts = memorySynthesisFlags{} }
	reset()
	t.Cleanup(reset)

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"memory", "synthesis", "ls", "--server", ts.URL, "-p", "p", "--day", "2026-10-28", "--state", "all", "-n", "5"}); code != 0 {
			t.Fatalf("ls exit=%d", code)
		}
	})
	if got := paths[len(paths)-1]; got != "/v1/memory-syntheses?day=2026-10-28&limit=5&project=p&state=all" {
		t.Fatalf("list request = %s", got)
	}
	if !strings.Contains(out, "ms-1 2026-10-28 p [succeeded] attempt 2 digest 0123456789ab") {
		t.Fatalf("ls output: %q", out)
	}

	reset()
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"memory", "synthesis", "show", "--server", ts.URL, "ms-1"}); code != 0 {
			t.Fatalf("show exit=%d", code)
		}
	})
	if paths[len(paths)-1] != "/v1/memory-syntheses/ms-1?" {
		t.Fatalf("show request = %s", paths[len(paths)-1])
	}
	for _, want := range []string{"status: succeeded", `materials: {"job_report":2}`, `result: {"summary":"ok"}`, "material snapshot: kept"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "never printed") {
		t.Fatalf("the snapshot body must not be printed without --json: %q", out)
	}

	reset()
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"memory", "synthesis", "show", "--server", ts.URL, "--json", "ms-1"}); code != 0 {
			t.Fatalf("show --json exit=%d", code)
		}
	})
	var got jobstore.MemorySynthesisRun
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.ID != "ms-1" || got.MaterialSnapshot == "" {
		t.Fatalf("json output: %q err=%v", out, err)
	}
}

func TestMemorySynthesisTextIsBilingual(t *testing.T) {
	r := jobstore.MemorySynthesisRun{ID: "ms-1", ProjectKey: "p", Day: "2026-10-28", Status: "failed", Attempt: 1, InputDigest: "abc"}
	t.Setenv("GOFER_LANG", string(i18n.EN))
	en := memorySynthesisLine(r)
	t.Setenv("GOFER_LANG", string(i18n.ZhCN))
	zh := memorySynthesisLine(r)
	if han.MatchString(en) || !han.MatchString(zh) || en == zh || !strings.Contains(zh, "ms-1") {
		t.Fatalf("en=%q zh=%q", en, zh)
	}
	if desc := cliText("cli.memory.synthesis.desc"); !han.MatchString(desc) {
		t.Fatalf("zh desc=%q", desc)
	}
}

func TestMemorySynthesisRunCommand(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.EN))
	t.Setenv("GOFER_PROJECT", "")
	var bodies []string
	var reply func() (int, any)
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/memory-syntheses/run" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		bodies = append(bodies, string(raw))
		status, payload := reply()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	})
	reset := func() { memorySynthesisOpts = memorySynthesisFlags{} }
	reset()
	t.Cleanup(reset)
	runJSON := `{"schema":"daily-memory-synthesis.v1","summary":"things landed","highlights":[{"text":"feature shipped","source_refs":["work:w-1"]}],"open_questions":["what next"]}`
	ok := jobstore.MemorySynthesisRun{ID: "ms-9", ProjectKey: "p", Day: "2026-10-28", Status: "succeeded", ResultJSON: runJSON}
	reply = func() (int, any) { return http.StatusOK, map[string]any{"outcome": "succeeded", "run": ok} }

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"memory", "synthesis", "run", "--server", ts.URL, "-p", "p", "--day", "2026-10-28", "--force"}); code != 0 {
			t.Fatalf("run exit=%d", code)
		}
	})
	if got := bodies[len(bodies)-1]; got != `{"project":"p","day":"2026-10-28","force":true}` {
		t.Fatalf("request body = %s", got)
	}
	for _, want := range []string{"synthesis ms-9 for p 2026-10-28: succeeded", "things landed", "- feature shipped [work:w-1]", "? what next"} {
		if !strings.Contains(out, want) {
			t.Fatalf("run output missing %q: %q", want, out)
		}
	}

	// Reused is reported with the --force hint; a failed run shows its reason.
	reset()
	reply = func() (int, any) { return http.StatusOK, map[string]any{"outcome": "reused", "run": ok} }
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"memory", "synthesis", "run", "--server", ts.URL, "-p", "p"}); code != 0 {
			t.Fatalf("exit=%d", code)
		}
	})
	if !strings.Contains(out, "reused (use --force to run again)") || strings.Contains(bodies[len(bodies)-1], "force") {
		t.Fatalf("reused output=%q body=%s", out, bodies[len(bodies)-1])
	}
	reset()
	failed := jobstore.MemorySynthesisRun{ID: "ms-10", ProjectKey: "p", Day: "2026-10-28", Status: "failed", Error: "invalid output: no JSON object in the answer"}
	reply = func() (int, any) { return http.StatusOK, map[string]any{"outcome": "failed", "run": failed} }
	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"memory", "synthesis", "run", "--server", ts.URL, "-p", "p", "--json"}); code != 0 {
			t.Fatalf("exit=%d", code)
		}
	})
	if !strings.Contains(out, `"outcome": "failed"`) && !strings.Contains(out, `"outcome":"failed"`) {
		t.Fatalf("json output=%q", out)
	}

	// Disabled: the server's refusal (with the config hint) reaches the person.
	reset()
	reply = func() (int, any) {
		return http.StatusConflict, map[string]string{"error": "memory synthesis disabled", "detail": "set memory.synthesis.enabled to true"}
	}
	var code int
	errOut := captureOutput(t, func() {
		code = NewApp("test").Run([]string{"memory", "synthesis", "run", "--server", ts.URL, "-p", "p"})
	})
	if code == 0 {
		t.Fatalf("a disabled synthesis must exit non-zero: %q", errOut)
	}

	// No project: refused locally, without a request.
	reset()
	n := len(bodies)
	code = NewApp("test").Run([]string{"memory", "synthesis", "run", "--server", ts.URL})
	if code == 0 || len(bodies) != n {
		t.Fatalf("code=%d requests=%d->%d", code, n, len(bodies))
	}
}

func TestMemorySynthesisRunTextIsBilingual(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.EN))
	en := cliText("cli.memory.synthesis.run.outcome.reused", "id", "ms-1", "day", "d", "project", "p", "error", "")
	t.Setenv("GOFER_LANG", string(i18n.ZhCN))
	zh := cliText("cli.memory.synthesis.run.outcome.reused", "id", "ms-1", "day", "d", "project", "p", "error", "")
	if han.MatchString(en) || !han.MatchString(zh) || !strings.Contains(zh, "ms-1") || !strings.Contains(zh, "--force") {
		t.Fatalf("en=%q zh=%q", en, zh)
	}
}
