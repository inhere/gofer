package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestJobRunUploadStagesBeforeSubmit: `job run --upload` stages the local file with
// the transfer API FIRST and submits the job with the returned xfer id — the server
// never learns a client-side path, and the executing machine fetches the bytes when
// the job starts (XFER-01 X2).
func TestJobRunUploadStagesBeforeSubmit(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	jobRunOpts.upload, jobRunOpts.collect = nil, nil
	t.Cleanup(func() { jobRunOpts.upload, jobRunOpts.collect = nil, nil })

	payload := []byte("firmware-payload")
	local := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(local, payload, 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	wantSum := sha256.Sum256(payload)

	var (
		mu    sync.Mutex
		order []string
		meta  map[string]any
		got   []byte
		body  job.JobRequest
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/xfer/precheck":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/xfer":
			mr, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart reader: %v", err)
				return
			}
			first, err := mr.NextPart()
			if err != nil {
				t.Errorf("meta part: %v", err)
				return
			}
			raw, _ := io.ReadAll(first)
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Errorf("meta is not JSON: %v (%s)", err, raw)
			}
			second, err := mr.NextPart()
			if err != nil {
				t.Errorf("file part: %v", err)
				return
			}
			got, _ = io.ReadAll(second)
			mu.Lock()
			order = append(order, "xfer")
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "xf-9", "state": "staged"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode job request: %v", err)
			}
			mu.Lock()
			order = append(order, "jobs")
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-1", Status: job.StatusQueued})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	code := NewApp("test").Run([]string{
		"job", "run", "-p", "self", "-a", "exec",
		"--upload", local + ":tmp/in/a.bin",
		"--server", ts.URL,
		"--", "go", "version",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "xfer,jobs" {
		t.Fatalf("request order = %v, want the staging call before the submit", order)
	}
	if meta["op"] != "put" || meta["project"] != "self" || meta["path"] != "tmp/in/a.bin" {
		t.Fatalf("staged meta = %v, want a put of tmp/in/a.bin in project self", meta)
	}
	if meta["runner"] != "local" {
		t.Fatalf("staged runner = %v, want the normalized local runner the executing machine is", meta["runner"])
	}
	// stage_only keeps the server from dispatching the payload anywhere: the job's
	// executing machine fetches it, at the job's own cwd.
	if meta["stage_only"] != true {
		t.Fatalf("staged meta = %v, want stage_only:true", meta)
	}
	if size, _ := meta["size"].(float64); int64(size) != int64(len(payload)) {
		t.Fatalf("staged size = %v, want %d", meta["size"], len(payload))
	}
	if meta["sha256"] != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("staged sha256 = %v, want the file's digest", meta["sha256"])
	}
	if string(got) != string(payload) {
		t.Fatalf("staged bytes = %q, want %q", got, payload)
	}
	if len(body.Uploads) != 1 || body.Uploads[0].XferID != "xf-9" || body.Uploads[0].Dest != "tmp/in/a.bin" {
		t.Fatalf("submitted uploads = %+v, want the staged id + the destination", body.Uploads)
	}
}

// TestJobRunCollectFlag: `--collect` rides the request as globs (repeatable) and
// stages nothing — the globs are matched on the executing machine after the job.
func TestJobRunCollectFlag(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	jobRunOpts.upload, jobRunOpts.collect = nil, nil
	t.Cleanup(func() { jobRunOpts.upload, jobRunOpts.collect = nil, nil })

	var body job.JobRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode job request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(job.JobResult{ID: "job-1", Status: job.StatusQueued})
	}))
	defer ts.Close()

	code := NewApp("test").Run([]string{
		"job", "run", "-p", "self", "-a", "exec",
		"--collect", "tmp/out/*.csv", "--collect", "*.log",
		"--server", ts.URL,
		"--", "go", "version",
	})
	if code != 0 {
		t.Fatalf("app.Run exit code=%d", code)
	}
	if strings.Join(body.Collect, ",") != "tmp/out/*.csv,*.log" {
		t.Fatalf("submitted collect = %v, want both globs in order", body.Collect)
	}
	if len(body.Uploads) != 0 {
		t.Fatalf("collect must not stage anything, got uploads=%+v", body.Uploads)
	}
}

// TestParseUploadSpec pins the --upload grammar: everything before the LAST colon is
// the local file, everything after it is the project-relative destination. The last
// colon (not the first) is what keeps a Windows path — `D:\in\a.bin:tmp/a.bin` — from
// being read as a drive-letter-prefixed destination.
func TestParseUploadSpec(t *testing.T) {
	cases := []struct {
		spec        string
		local, dest string
		wantErr     bool
	}{
		{spec: "./a.bin:tmp/in/a.bin", local: "./a.bin", dest: "tmp/in/a.bin"},
		{spec: `D:\in\a.bin:tmp/in/a.bin`, local: `D:\in\a.bin`, dest: "tmp/in/a.bin"},
		{spec: "a.bin", wantErr: true},
		{spec: "a.bin:", wantErr: true},
		{spec: ":tmp/a.bin", wantErr: true},
	}
	for _, tc := range cases {
		local, dest, err := parseUploadSpec(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseUploadSpec(%q) = %q,%q, want an error", tc.spec, local, dest)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseUploadSpec(%q): %v", tc.spec, err)
		}
		if local != tc.local || dest != tc.dest {
			t.Fatalf("parseUploadSpec(%q) = %q,%q, want %q,%q", tc.spec, local, dest, tc.local, tc.dest)
		}
	}
}
