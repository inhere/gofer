package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gookit/gcli/v3"
)

// tool_api_test.go drives `gofer tool api` against an httptest stub (G053: no real
// server): path spelling, field handling, the query/body split, --query extraction and
// the non-2xx contract.

type apiStub struct {
	mu       sync.Mutex
	requests []settingsStubRequest
	status   int
	body     string
	headers  map[string]string
}

func (s *apiStub) last(t *testing.T) settingsStubRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatalf("no request reached the stub")
	}
	return s.requests[len(s.requests)-1]
}

func (s *apiStub) server(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, settingsStubRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Body: string(data), ContentType: r.Header.Get("Content-Type"),
		})
		status, body, headers := s.status, s.body, s.headers
		s.mu.Unlock()
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	jobConnOpts.server = srv.URL
	jobConnOpts.token = "tkn"
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
}

// resetToolApiOpts clears the package-level option vars between cases.
func resetToolApiOpts(t *testing.T) {
	t.Helper()
	toolApiOpts.method, toolApiOpts.input, toolApiOpts.query = "", "", ""
	toolApiOpts.showHeader = false
	toolApiOpts.fields, toolApiOpts.files = nil, nil
	t.Cleanup(func() {
		toolApiOpts.method, toolApiOpts.input, toolApiOpts.query = "", "", ""
		toolApiOpts.showHeader = false
		toolApiOpts.fields, toolApiOpts.files = nil, nil
	})
}

func TestNormalizeAPIPath(t *testing.T) {
	cases := map[string]string{
		"jobs":             "/v1/jobs",
		"/jobs":            "/v1/jobs",
		"/v1/jobs":         "/v1/jobs",
		"v1/jobs":          "/v1/jobs",
		"/v1":              "/v1",
		"jobs?status=done": "/v1/jobs?status=done",
		"":                 "",
		"  settings/foo  ": "/v1/settings/foo",
		"/v1/settings/x?y": "/v1/settings/x?y",
	}
	for in, want := range cases {
		if got := normalizeAPIPath(in); got != want {
			t.Errorf("normalizeAPIPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToolApiGetFieldsBecomeQuery(t *testing.T) {
	resetToolApiOpts(t)
	stub := &apiStub{body: `{"jobs":[{"id":"j1"},{"id":"j2"}]}`}
	stub.server(t)
	c := bindToolApi(t)
	toolApiOpts.fields = gcli.Strings{"status=done", "limit=5"}
	out := captureOutput(t, func() {
		if err := runToolApi(c, []string{"jobs"}); err != nil {
			t.Fatalf("tool api jobs: %v", err)
		}
	})
	req := stub.last(t)
	if req.Method != http.MethodGet || req.Path != "/v1/jobs" {
		t.Fatalf("a GET with -f should stay a GET, got %s %s", req.Method, req.Path)
	}
	if !strings.Contains(req.Query, "status=done") || !strings.Contains(req.Query, "limit=5") {
		t.Fatalf("fields did not become query parameters: %q", req.Query)
	}
	// The JSON response is re-indented for a terminal.
	if !strings.Contains(out, "\"id\": \"j1\"") {
		t.Fatalf("response not printed as indented JSON: %q", out)
	}
}

func TestToolApiBodyFromFieldsFilesAndInput(t *testing.T) {
	resetToolApiOpts(t)
	stub := &apiStub{body: `{"ok":true}`}
	stub.server(t)
	c := bindToolApi(t)

	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte("multi\nline"), 0o644); err != nil {
		t.Fatalf("write field file: %v", err)
	}
	toolApiOpts.method = "PUT"
	toolApiOpts.fields = gcli.Strings{"value=3", "on=true", "name=plain"}
	toolApiOpts.files = gcli.Strings{"prompt=@" + path}
	if err := runToolApi(c, []string{"settings/x"}); err != nil {
		t.Fatalf("tool api PUT: %v", err)
	}
	req := stub.last(t)
	// -X picks the method; the fields then build the JSON body, with values typed as
	// JSON where they parse as JSON.
	if req.Method != http.MethodPut || req.ContentType != "application/json" {
		t.Fatalf("fields should build a JSON body on a write, got %s %q", req.Method, req.ContentType)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(req.Body), &sent); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, req.Body)
	}
	if sent["value"] != float64(3) || sent["on"] != true || sent["name"] != "plain" || sent["prompt"] != "multi\nline" {
		t.Fatalf("unexpected body fields: %+v", sent)
	}

	// An explicit --input wins over the fields and keeps the bytes verbatim.
	inputPath := filepath.Join(t.TempDir(), "raw.json")
	if err := os.WriteFile(inputPath, []byte(`{"raw":1}`), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	toolApiOpts.input = inputPath
	toolApiOpts.method = ""
	toolApiOpts.fields = nil
	if err := runToolApi(c, []string{"jobs"}); err != nil {
		t.Fatalf("tool api --input: %v", err)
	}
	if req := stub.last(t); req.Method != http.MethodPost || req.Body != `{"raw":1}` {
		t.Fatalf("--input body not sent verbatim, got %s %q", req.Method, req.Body)
	}
}

func TestToolApiQueryExtraction(t *testing.T) {
	resetToolApiOpts(t)
	stub := &apiStub{body: `{"jobs":[{"id":"j1"},{"id":"j2"}],"name":"world"}`}
	stub.server(t)
	c := bindToolApi(t)

	toolApiOpts.query = "name"
	out := captureOutput(t, func() {
		if err := runToolApi(c, []string{"jobs"}); err != nil {
			t.Fatalf("tool api -q name: %v", err)
		}
	})
	if out != "world\n" {
		t.Fatalf("a string value prints as it is, got %q", out)
	}

	toolApiOpts.query = "jobs.1.id"
	out = captureOutput(t, func() {
		if err := runToolApi(c, []string{"jobs"}); err != nil {
			t.Fatalf("tool api -q jobs.1.id: %v", err)
		}
	})
	if out != "j2\n" {
		t.Fatalf("array index path, got %q", out)
	}

	toolApiOpts.query = "jobs.*.id"
	out = captureOutput(t, func() {
		if err := runToolApi(c, []string{"jobs"}); err != nil {
			t.Fatalf("tool api -q jobs.*.id: %v", err)
		}
	})
	if !strings.Contains(out, "j1") || !strings.Contains(out, "j2") {
		t.Fatalf("wildcard path should collect every id, got %q", out)
	}

	// A path the response does not have is a failure, not an empty line.
	toolApiOpts.query = "jobs.9.id"
	err := runToolApi(c, []string{"jobs"})
	if err == nil || !strings.Contains(err.Error(), "no such value") {
		t.Fatalf("a missing path must fail, got %v", err)
	}
	assertCodedExit(t, err)
}

func TestToolApiPassesErrorsThrough(t *testing.T) {
	resetToolApiOpts(t)
	stub := &apiStub{status: http.StatusForbidden, body: `{"error":"forbidden","detail":"settings.write is not granted"}`}
	stub.server(t)
	c := bindToolApi(t)
	stderr := captureToolApiStderr(t)

	var err error
	out := captureOutput(t, func() { err = runToolApi(c, []string{"/v1/settings/work.draft.min_prompts"}) })
	if err == nil {
		t.Fatal("a non-2xx answer must exit non-zero")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("the error should name the status, got %v", err)
	}
	assertCodedExit(t, err)
	// Nothing of the error body goes to stdout; it goes to stderr verbatim.
	if strings.Contains(out, "settings.write is not granted") {
		t.Fatalf("the error body leaked to stdout: %q", out)
	}
	if !strings.Contains(stderr.String(), "settings.write is not granted") {
		t.Fatalf("the error body should reach stderr, got %q", stderr.String())
	}
}

func TestToolApiIncludePrintsStatusAndHeaders(t *testing.T) {
	resetToolApiOpts(t)
	stub := &apiStub{body: `{"ok":true}`, headers: map[string]string{"X-Gofer-Test": "yes"}}
	stub.server(t)
	c := bindToolApi(t)
	toolApiOpts.showHeader = true
	out := captureOutput(t, func() {
		if err := runToolApi(c, []string{"v1/meta"}); err != nil {
			t.Fatalf("tool api -i: %v", err)
		}
	})
	if !strings.Contains(out, "HTTP 200 OK") || !strings.Contains(out, "X-Gofer-Test: yes") {
		t.Fatalf("-i should print the status line and headers, got %q", out)
	}
	if req := stub.last(t); req.Path != "/v1/meta" {
		t.Fatalf("the /v1 prefix should be added once, got %q", req.Path)
	}
}

// captureToolApiStderr swaps toolApiStderr for a buffer and returns it.
func captureToolApiStderr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := toolApiStderr
	toolApiStderr = &buf
	t.Cleanup(func() { toolApiStderr = prev })
	return &buf
}
