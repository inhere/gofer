package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobResumeLockFlag: `job resume --lock` (gofer-jkd2) reaches the server as
// lock_paths in the resume body, and stays absent when the flag is not given (the
// continuation then inherits the source's declaration).
func TestJobResumeLockFlag(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	// gcli does not clear an unset flag: start clean and leave nothing behind.
	jobResumeOpts.lock = nil
	t.Cleanup(func() { jobResumeOpts.lock = nil })

	var got map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs/src-1/resume" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		got = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode resume body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-next", Status: job.StatusQueued})
	}))
	defer ts.Close()

	app := NewApp("test")
	if code := app.Run([]string{"job", "resume", "src-1", "--prompt", "narrow it", "--lock", "gookit/ini", "--lock", "gookit/cli", "--server", ts.URL}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if gotPaths, ok := got["lock_paths"].([]any); !ok {
		t.Fatalf("resume body must carry lock_paths: %v", got)
	} else {
		paths := make([]string, 0, len(gotPaths))
		for _, v := range gotPaths {
			paths = append(paths, v.(string))
		}
		if !reflect.DeepEqual(paths, []string{"gookit/ini", "gookit/cli"}) {
			t.Fatalf("resume body lock_paths = %#v, want the two --lock values", paths)
		}
	}

	jobResumeOpts.lock = nil
	got = nil
	if code := app.Run([]string{"job", "resume", "src-1", "--prompt", "again", "--server", ts.URL}); code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if _, has := got["lock_paths"]; has {
		t.Fatalf("without --lock the body must not carry lock_paths: %v", got)
	}
}
