package job

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// updateDispatchGolden regenerates testdata/plan_dispatch_golden/*.json
// (go test ./internal/job -run TestPlanDispatchGolden -update-dispatch-golden, or
// GOFER_UPDATE_GOLDEN=1).
var updateDispatchGolden = flag.Bool("update-dispatch-golden", false, "rewrite the plan todo dispatch golden files")

// The golden files pin what a NORMAL plan-todo dispatch persists — the request_json
// Submit stores and the job row — so that extracting Submit's pieces (resume checks,
// template rendering, prompt sections) cannot silently change a dispatched job. Volatile
// values (job / request ids, timestamps, the temp root, the test binary path) are
// normalised before comparing.

type dispatchGoldenCase struct {
	name string
	// plan / todo are seeded as given (PlanID / TodoID are filled by the test).
	plan       jobstore.Plan
	todo       jobstore.PlanTodo
	template   string // task book body written under the project, "" = none
	supervisor bool   // seed the supervising session the plan binds
}

func TestPlanDispatchGolden(t *testing.T) {
	t.Parallel()
	bin := testcmd.Path(t)
	cases := []dispatchGoldenCase{
		{
			name: "no_template",
			todo: jobstore.PlanTodo{Title: "plain item", Note: "plain note", Assignee: "omp", Runner: "local"},
		},
		{
			name: "empty_runner",
			todo: jobstore.PlanTodo{Title: "no runner item", Assignee: "omp"},
		},
		{
			name:     "template_vars",
			template: "Plan: {{plan_title}}\nTodo: {{todo_title}} ({{todo_id}})\nNote: {{todo_note}}\nExtra: {{extra}}\n",
			todo: jobstore.PlanTodo{
				Title: "tpl item", Note: "tpl note", Assignee: "omp", Template: "golden-tpl",
				Vars: map[string]string{"extra": "E"},
			},
		},
		{
			name: "exec_cmd",
			todo: jobstore.PlanTodo{Title: "exec item", Assignee: agent.ExecAgentKey, Cmd: []string{bin, "exit", "0"}},
		},
		{
			name:       "supervisor_notify",
			plan:       jobstore.Plan{Owner: "alice", Notify: jobstore.PlanNotifyOn},
			supervisor: true,
			todo:       jobstore.PlanTodo{Title: "notify item", Assignee: "omp"},
		},
		{
			name: "full_contract",
			todo: jobstore.PlanTodo{
				Title: "contract item", Note: "contract note", Assignee: "rich", Runner: "local",
				Acceptance:    "the output mentions the item\nsecond criterion",
				Scope:         []string{"internal/job/**", "docs/*.md"},
				Verify:        []string{bin, "exit", "0"},
				Review:        true,
				Budget:        &config.Budget{MaxTokens: 1000, MaxCostUSD: 0.5, MaxTurns: 7},
				Model:         "model-x",
				PrimeKeywords: []string{"alpha", "beta"},
				TimeoutSec:    600,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := newGoldenDispatchService(t, root, bin)
			if tc.template != "" {
				writeTemplate(t, root, tc.todo.Template, tc.template)
			}
			plan := tc.plan
			plan.PlanID, plan.Title, plan.Description = "plan-g", "Golden plan", "golden desc"
			plan.Status, plan.ProjectKey, plan.CreatedAt, plan.UpdatedAt = jobstore.PlanOpen, "self", 1, 1
			if tc.supervisor {
				plan.SupervisorSessionID = "golden-sup"
				if _, err := s.meta.UpsertAgentSession(jobstore.AgentSession{
					SessionID: "golden-sup", Agent: "claude", ProjectKey: "self",
					Runner: "local", Cwd: root, CallerID: plan.Owner,
				}); err != nil {
					t.Fatalf("seed supervisor session: %v", err)
				}
			}
			todo := tc.todo
			todo.TodoID, todo.CreatedAt, todo.UpdatedAt = "todo-g", 1, 1
			seedDispatchPlan(t, s, plan, todo)
			setTodoStatus(t, s, "todo-g", jobstore.TodoReady)

			by := "alice"
			d, err := s.MaybeDispatchTodo("todo-g", by)
			if err != nil || d.Job == nil {
				t.Fatalf("dispatch: %+v err=%v", d, err)
			}
			if _, ok := s.Wait(d.Job.ID); !ok {
				t.Fatalf("Wait: job %s not found", d.Job.ID)
			}
			rec, ok, err := s.meta.GetJob(d.Job.ID)
			if err != nil || !ok {
				t.Fatalf("GetJob(%s): ok=%v err=%v", d.Job.ID, ok, err)
			}
			got := normalizeDispatchGolden(t, rec, root, bin)
			path := filepath.Join("testdata", "plan_dispatch_golden", tc.name+".json")
			if *updateDispatchGolden || os.Getenv("GOFER_UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s (regenerate with -update-dispatch-golden): %v", path, err)
			}
			if string(want) != string(got) {
				t.Fatalf("dispatch of %s drifted from %s\n--- want\n%s\n--- got\n%s", tc.name, path, want, got)
			}
		})
	}
}

func newGoldenDispatchService(t *testing.T, root, bin string) *Service {
	t.Helper()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"omp", "rich", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"omp": {Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"}},
			// "rich" takes a model and reports usage, so a model / budget dispatch is admitted.
			"rich": {
				Type: agent.TypeCLIAgent, Command: bin, Args: []string{"argv", "{{prompt}}"},
				ModelArgs:    []string{"--model", "{{model}}"},
				OutputFormat: config.OutputFormatNDJSON, NDJSONStdoutPath: "result", NDJSONUsagePath: "usage",
			},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

var (
	goldenDurationPattern = regexp.MustCompile(`"duration_ms":\d+`)

	// goldenPathTailPattern matches a placeholder and the path segments after it, with
	// either separator (a JSON-escaped backslash is two characters).
	goldenDatePattern     = regexp.MustCompile(`/\d{8}/<job>`)
	goldenPathTailPattern = regexp.MustCompile(`<(?:root|bin)>(?:(?:\\\\|\\|/)[^"\\/\s]*)+`)
)

// normalizeDispatchGolden renders the persisted request_json and job row as stable
// indented JSON with the volatile values replaced by placeholders.
func normalizeDispatchGolden(t *testing.T, rec jobstore.JobRecord, root, bin string) []byte {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(rec.RequestJSON), &req); err != nil {
		t.Fatalf("request_json is not JSON: %v", err)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	// Run output and bookkeeping that depend on timing, not on the dispatch.
	for _, k := range []string{"RequestJSON", "ResultJSON", "ArtifactsJSON", "DiffSummary", "SessionStateJSON"} {
		delete(row, k)
	}
	for k, v := range row {
		if strings.HasSuffix(k, "At") {
			if n, _ := v.(float64); n != 0 {
				row[k] = "<time>"
			}
		}
	}
	if rec.RequestID != "" {
		row["RequestID"] = "<request-id>"
	}
	for _, k := range []string{"request_id", "RequestID"} {
		if _, ok := req[k]; ok {
			req[k] = "<request-id>"
		}
	}
	norm := func(s string) string {
		s = goldenDurationPattern.ReplaceAllString(s, `"duration_ms":<ms>`)
		s = strings.ReplaceAll(s, rec.ID, "<job>")
		// Paths also appear inside JSON-encoded strings (RenderedCommand, VerifyJSON),
		// where a Windows backslash is escaped once more: replace that spelling first.
		for _, p := range []struct{ path, tag string }{{root, "<root>"}, {bin, "<bin>"}} {
			s = strings.ReplaceAll(s, strings.ReplaceAll(p.path, `\`, `\\`), p.tag)
			s = strings.ReplaceAll(s, filepath.ToSlash(p.path), p.tag)
			s = strings.ReplaceAll(s, p.path, p.tag)
		}
		// The rest of a path after a placeholder keeps the OS separator (a Windows
		// ResultDir reads <root>\self\…); normalise it to "/" so one golden fits all.
		s = goldenPathTailPattern.ReplaceAllStringFunc(s, func(m string) string {
			return strings.ReplaceAll(strings.ReplaceAll(m, `\\`, "/"), `\`, "/")
		})
		// The result dir carries the run date (<root>/<caller>/YYYYMMDD/<job>): a golden
		// recorded on one day must still match after midnight.
		s = goldenDatePattern.ReplaceAllString(s, "/<date>/<job>")
		return s
	}
	out := map[string]any{"request": walkStrings(req, norm), "job": walkStrings(row, norm)}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// walkStrings applies fn to every string in a decoded JSON tree.
func walkStrings(v any, fn func(string) string) any {
	switch x := v.(type) {
	case string:
		return fn(x)
	case []any:
		for i := range x {
			x[i] = walkStrings(x[i], fn)
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = walkStrings(x[k], fn)
		}
		return x
	}
	return v
}
