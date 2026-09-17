package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobInteractionsListsPermissionAndAnswers drives the two interaction subcommands
// against a stub server: `job interactions` must surface an approval card's gated tool
// call and the option ids an answer may name, and `job answer` must POST the chosen
// ACP option id (the agent's own option, relayed verbatim).
func TestJobInteractionsListsPermissionAndAnswers(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-1/interactions":
			_ = json.NewEncoder(w).Encode(map[string]any{"interactions": []job.Interaction{{
				ID:     "abcd1234",
				JobID:  "job-1",
				Type:   job.InteractionTypePermission,
				Prompt: "Approve tool call \"Write main.go\" (kind=edit)",
				Options: []job.InteractionOption{
					{Value: "allow-once-id", ID: "allow-once-id", Label: "Allow once", Kind: "allow_once"},
					{Value: "reject-once-id", ID: "reject-once-id", Label: "Reject", Kind: "reject_once"},
				},
				Status: job.InteractionPending,
				ToolCall: &job.InteractionToolCall{
					ID:              "tc-1",
					Title:           "Write main.go",
					Kind:            "edit",
					Locations:       []string{"main.go"},
					RawInputSummary: `{"path":"main.go"}`,
				},
				PolicyHint: "ask: kind=edit",
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs/job-1/interactions/abcd1234/answer":
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_ = json.NewEncoder(w).Encode(job.Interaction{
				ID: "abcd1234", Status: job.InteractionAnswered, Answer: "allow-once-id", AnsweredBy: "human",
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "interactions", "--server", ts.URL, "job-1"}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	for _, want := range []string{
		"abcd1234", "pending", "permission",
		`[edit] "Write main.go" @main.go`, `input={"path":"main.go"}`,
		"ask: kind=edit",
		"Allow once=allow-once-id", "Reject=reject-once-id",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("`job interactions` output missing %q:\n%s", want, out)
		}
	}

	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "answer", "--server", ts.URL, "job-1", "abcd1234", "allow-once-id"}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(gotBody, `"answer":"allow-once-id"`) {
		t.Fatalf("`job answer` posted %q, want the chosen option id", gotBody)
	}
	if !strings.Contains(out, "answered: allow-once-id") {
		t.Fatalf("`job answer` output = %q, want it to echo the recorded answer", out)
	}
}
