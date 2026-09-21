package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// resetWakeupFlagGlobals clears the `job wakeup` flag struct, which is a package
// global gcli does not reset between Run calls (same treatment as jobRunOpts).
func resetWakeupFlagGlobals() {
	jobWakeupOpts.kind, jobWakeupOpts.after, jobWakeupOpts.at = "", "", ""
	jobWakeupOpts.every, jobWakeupOpts.cron, jobWakeupOpts.tz = "", "", ""
	jobWakeupOpts.event, jobWakeupOpts.filterJob, jobWakeupOpts.status = "", "", ""
	jobWakeupOpts.mode, jobWakeupOpts.instruction, jobWakeupOpts.file = "", "", ""
}

// wakeupCreateServer serves POST /v1/jobs/{id}/wakeups and records what arrived.
func wakeupCreateServer(t *testing.T, got *job.WakeupSpec, gotPath *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotPath = r.Method + " " + r.URL.Path
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(client.Wakeup{
			ID: "wk-1", JobID: "job-1", Kind: got.Kind, Enabled: true, Mode: "once",
		})
	}))
}

// TestJobWakeupCreateFlags: each kind's flags become the right request body — the
// four shapes are the whole CLI surface, so one table pins them all.
func TestJobWakeupCreateFlags(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	resetWakeupFlagGlobals()
	t.Cleanup(resetWakeupFlagGlobals)

	instr := filepath.Join(t.TempDir(), "instr.md")
	if err := os.WriteFile(instr, []byte("look at the queue\n"), 0o600); err != nil {
		t.Fatalf("write instruction file: %v", err)
	}
	atInstant := "2030-01-02T03:04:05Z"
	wantAt, err := time.Parse(time.RFC3339, atInstant)
	if err != nil {
		t.Fatalf("parse fixture instant: %v", err)
	}

	cases := []struct {
		name string
		args []string
		want func(t *testing.T, spec job.WakeupSpec)
	}{
		{
			name: "--after",
			args: []string{"--kind", "at", "--after", "10m", "-m", "check the CI result"},
			want: func(t *testing.T, spec job.WakeupSpec) {
				if spec.Kind != jobstore.WakeupKindAt {
					t.Fatalf("kind = %q, want at", spec.Kind)
				}
				delta := spec.At - time.Now().Unix()
				if delta < 590 || delta > 610 {
					t.Fatalf("at = %d, want now+10m (delta %ds)", spec.At, delta)
				}
				if spec.Instruction != "check the CI result" {
					t.Fatalf("instruction = %q", spec.Instruction)
				}
			},
		},
		{
			name: "--at",
			args: []string{"--kind", "at", "--at", atInstant, "-m", "go"},
			want: func(t *testing.T, spec job.WakeupSpec) {
				if spec.At != wantAt.Unix() {
					t.Fatalf("at = %d, want the RFC3339 instant %d", spec.At, wantAt.Unix())
				}
			},
		},
		{
			name: "--every",
			args: []string{"--kind", "every", "--every", "1h", "-m", "poll"},
			want: func(t *testing.T, spec job.WakeupSpec) {
				if spec.Kind != jobstore.WakeupKindEvery || spec.EverySec != 3600 {
					t.Fatalf("spec = %+v, want an hourly timer", spec)
				}
				// The default mode is the server's decision (continuous for timers),
				// so the CLI must NOT invent one.
				if spec.Mode != "" {
					t.Fatalf("mode = %q, want the CLI to leave the default to the server", spec.Mode)
				}
			},
		},
		{
			name: "--cron --tz",
			args: []string{"--kind", "cron", "--cron", "0 9 * * 1-5", "--tz", "Asia/Shanghai", "-m", "morning check"},
			want: func(t *testing.T, spec job.WakeupSpec) {
				if spec.Cron != "0 9 * * 1-5" || spec.Timezone != "Asia/Shanghai" {
					t.Fatalf("spec = %+v, want the cron expression and its timezone", spec)
				}
			},
		},
		{
			name: "--event --job-id --status",
			args: []string{
				"--kind", "event", "--event", "job.terminal,job.stalled", "--job-id", "other-job",
				"--status", "done,failed", "--mode", "continuous", "-m", "react",
			},
			want: func(t *testing.T, spec job.WakeupSpec) {
				if len(spec.EventTypes) != 2 || spec.EventTypes[0] != "job.terminal" || spec.EventTypes[1] != "job.stalled" {
					t.Fatalf("event_types = %v, want both types in order", spec.EventTypes)
				}
				if spec.FilterJobID != "other-job" {
					t.Fatalf("filter_job_id = %q, want the watched job", spec.FilterJobID)
				}
				if len(spec.FilterStatus) != 2 || spec.FilterStatus[0] != "done" || spec.FilterStatus[1] != "failed" {
					t.Fatalf("filter_status = %v, want both statuses", spec.FilterStatus)
				}
				if spec.Mode != jobstore.WakeupModeContinuous {
					t.Fatalf("mode = %q, want continuous", spec.Mode)
				}
			},
		},
		{
			name: "-f instruction file",
			args: []string{"--kind", "at", "--after", "5m", "-f", instr},
			want: func(t *testing.T, spec job.WakeupSpec) {
				if spec.Instruction != "look at the queue" {
					t.Fatalf("instruction = %q, want the file's trimmed content", spec.Instruction)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetWakeupFlagGlobals()
			var spec job.WakeupSpec
			var gotPath string
			ts := wakeupCreateServer(t, &spec, &gotPath)
			defer ts.Close()

			out := captureOutput(t, func() {
				if code := NewApp("test").Run(append([]string{"job", "wakeup", "create", "job-1", "--server", ts.URL}, tc.args...)); code != 0 {
					t.Fatalf("app.Run exit code=%d", code)
				}
			})
			if gotPath != "POST /v1/jobs/job-1/wakeups" {
				t.Fatalf("request = %q, want a POST to the job's wakeups", gotPath)
			}
			tc.want(t, spec)
			if !strings.Contains(out, "wakeup wk-1 created on job job-1") {
				t.Fatalf("create output = %q, want it to report the new wakeup", out)
			}
		})
	}

	// Mutually exclusive / malformed input is refused client-side (no request).
	for _, args := range [][]string{
		{"--kind", "at", "--after", "5m", "--at", atInstant, "-m", "x"},
		{"--kind", "at", "--after", "5m", "-m", "x", "-f", instr},
		{"--after", "5m", "-m", "x"},
		{"--kind", "every", "--every", "soon", "-m", "x"},
		{"--kind", "at", "--at", "yesterday", "-m", "x"},
	} {
		resetWakeupFlagGlobals()
		var spec job.WakeupSpec
		var gotPath string
		ts := wakeupCreateServer(t, &spec, &gotPath)
		captureOutput(t, func() {
			if code := NewApp("test").Run(append([]string{"job", "wakeup", "create", "job-1", "--server", ts.URL}, args...)); code == 0 {
				t.Fatalf("app.Run accepted %v", args)
			}
		})
		if gotPath != "" {
			t.Fatalf("args %v sent %q, want a client-side refusal", args, gotPath)
		}
		ts.Close()
	}
}

// TestJobShowPrintsWakeups: `job show` states how many wakeups are still armed and
// of which kinds — the one line that answers "will this job run itself again?".
func TestJobShowPrintsWakeups(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/jobs/job-wk":
			_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-wk", ProjectKey: "self", Status: job.StatusDone})
		case "/v1/jobs/job-wk/wakeups":
			_ = json.NewEncoder(w).Encode(client.WakeupsResp{Wakeups: []client.Wakeup{
				{ID: "wk-1", Kind: "event", Enabled: true},
				{ID: "wk-2", Kind: "every", Enabled: true},
				{ID: "wk-3", Kind: "at", Enabled: false},
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "show", "job-wk", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "wakeups:    2 enabled (1 every, 1 event)") {
		t.Fatalf("job show output missing the wakeups summary:\n%s", out)
	}
}

// TestFormatWakeupsOmitsTheLineWhenEmpty: a job with no wakeups prints no line at all
// (the same rule the xfer/verify lines follow), and a job whose wakeups are all
// disabled says so rather than claiming zero.
func TestFormatWakeupsOmitsTheLineWhenEmpty(t *testing.T) {
	if got := formatWakeups(nil); got != "" {
		t.Fatalf("formatWakeups(nil) = %q, want no line", got)
	}
	got := formatWakeups([]client.Wakeup{{ID: "wk-1", Kind: "at", Enabled: false}})
	if got != "0 enabled (1 disabled)" {
		t.Fatalf("formatWakeups(disabled) = %q", got)
	}
	got = formatWakeups([]client.Wakeup{
		{ID: "wk-1", Kind: "at", Enabled: true},
		{ID: "wk-2", Kind: "at", Enabled: true},
		{ID: "wk-3", Kind: "cron", Enabled: true},
	})
	if got != "3 enabled (2 at, 1 cron)" {
		t.Fatalf("formatWakeups = %q, want the kind breakdown in kind order", got)
	}
}
