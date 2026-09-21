package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// TestScheduleCreateWebhookAndRotate (AUTO-02b): `schedule add --webhook` asks the server
// for the external trigger endpoint, the minted token is printed once at creation and
// stays readable on `schedule show`, and `schedule rotate-token` mints a fresh one.
func TestScheduleCreateWebhookAndRotate(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""

	const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var created map[string]any
	var rotatePath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/stats":
			// The CLI resolves the server's timezone once for its time rendering
			// (bd h-aii-tnua); the value is irrelevant here.
			_, _ = w.Write([]byte(`{"server_tz_offset_sec":0}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/schedules":
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Errorf("decode schedule body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(client.Schedule{
				ID: "sch-1", Name: "nightly", Type: "cron", Cron: "*/5 * * * *",
				Enabled: 1, CatchUp: 1, NextRunAt: 100, TriggerToken: token,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/schedules/sch-1":
			_ = json.NewEncoder(w).Encode(client.Schedule{
				ID: "sch-1", Name: "nightly", Type: "cron", Cron: "*/5 * * * *",
				Enabled: 1, TriggerToken: token,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/schedules/sch-1/rotate-token":
			rotatePath = r.URL.Path
			_ = json.NewEncoder(w).Encode(client.Schedule{
				ID: "sch-1", Name: "nightly", Type: "cron", Cron: "*/5 * * * *",
				Enabled: 1, TriggerToken: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	app := NewApp("test")
	out := captureOutput(t, func() {
		if code := app.Run([]string{
			"schedule", "add", "--server", ts.URL, "--name", "nightly", "--cron", "*/5 * * * *",
			"--project", "self", "--agent", "exec", "--webhook", "--", "go", "version",
		}); code != 0 {
			t.Fatalf("schedule add exit code=%d", code)
		}
	})
	if webhook, ok := created["webhook"].(bool); !ok || !webhook {
		t.Fatalf("create body webhook = %v, want true", created["webhook"])
	}
	if !strings.Contains(out, token) {
		t.Fatalf("create output does not print the webhook token:\n%s", out)
	}

	// `schedule show` is where the operator finds the token again.
	showOut := captureOutput(t, func() {
		if code := app.Run([]string{"schedule", "show", "--server", ts.URL, "sch-1"}); code != 0 {
			t.Fatalf("schedule show exit code=%d", code)
		}
	})
	if !strings.Contains(showOut, "webhook:") || !strings.Contains(showOut, token) {
		t.Fatalf("schedule show does not print the webhook token:\n%s", showOut)
	}

	rotateOut := captureOutput(t, func() {
		if code := app.Run([]string{"schedule", "rotate-token", "--server", ts.URL, "sch-1"}); code != 0 {
			t.Fatalf("schedule rotate-token exit code=%d", code)
		}
	})
	if rotatePath != "/v1/schedules/sch-1/rotate-token" {
		t.Fatalf("rotate path = %q", rotatePath)
	}
	if !strings.Contains(rotateOut, "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB") {
		t.Fatalf("rotate-token output does not print the new token:\n%s", rotateOut)
	}

	// A missing id is refused before any request.
	rotatePath = ""
	if code := app.Run([]string{"schedule", "rotate-token", "--server", ts.URL}); code == 0 {
		t.Fatal("schedule rotate-token without an id must fail")
	}
	if rotatePath != "" {
		t.Fatal("a refused rotate must not reach the server")
	}
}
