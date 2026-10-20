package commands

import (
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

// settings_test.go drives the `gofer settings` group against an httptest stub, so the
// CLI-to-API contract (path, method, body, preview vs write) is exercised without a
// real server (G053).

// settingsStub is the fake /v1/settings API: canned answers plus a record of the
// requests it saw.
type settingsStub struct {
	mu       sync.Mutex
	requests []settingsStubRequest
	items    []map[string]any
	// putItem is the answer of a PUT; setImport the answer of POST /import.
	putItem   map[string]any
	setImport map[string]any
}

type settingsStubRequest struct {
	Method      string
	Path        string
	Query       string
	Body        string
	ContentType string
}

func (s *settingsStub) last() settingsStubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return settingsStubRequest{}
	}
	return s.requests[len(s.requests)-1]
}

func (s *settingsStub) server(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, settingsStubRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Body: string(body), ContentType: r.Header.Get("Content-Type"),
		})
		s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"settings": s.items})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/settings/export":
			w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
			_, _ = w.Write([]byte("work.draft.min_prompts: 3\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/settings/import":
			_ = json.NewEncoder(w).Encode(s.setImport)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/settings/"):
			key := strings.TrimPrefix(r.URL.Path, "/v1/settings/")
			for _, it := range s.items {
				if it["key"] == key {
					_ = json.NewEncoder(w).Encode(it)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unknown setting", "detail": "no setting named " + key})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/settings/"):
			_ = json.NewEncoder(w).Encode(s.putItem)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/settings/"):
			key := strings.TrimPrefix(r.URL.Path, "/v1/settings/")
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "modified": false, "value": 2, "reset": true})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
		}
	}))
	t.Cleanup(srv.Close)
	jobConnOpts.server = srv.URL
	jobConnOpts.token = "tkn"
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	return srv, srv.Close
}

// settingsStubItems is the registry the stub serves: one int key, one list key and one
// prompt key, i.e. the three shapes the CLI renders differently.
func settingsStubItems() []map[string]any {
	return []map[string]any{
		{"key": "work.draft.min_prompts", "kind": "int", "default": 2, "value": 2, "modified": false, "description": "prompts before a draft", "min": 1, "max": 20},
		{"key": "work.draft.exclude_projects", "kind": "strings", "default": []string{}, "value": []string{"demo", "sandbox"}, "modified": true},
		{"key": "prompt.work.summarizer", "kind": "string", "default": "summarize", "value": "line one\nline two", "modified": true, "multiline": true},
	}
}

func TestSettingsCmdRegistered(t *testing.T) {
	app := NewApp("test")
	cmd := app.GetCommand("settings")
	if cmd == nil {
		t.Fatal("`gofer settings` is not registered on the app")
	}
	for _, name := range []string{"ls", "get", "set", "reset", "export", "import"} {
		found := false
		for _, sub := range cmd.Subs {
			if sub.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("`gofer settings` is missing the %q sub-command", name)
		}
	}
}

func TestSettingsLsTableAndJSON(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems()}
	stub.server(t)
	c := bindCmd(NewSettingsCmd().Subs[0]) // ls

	out := captureOutput(t, func() {
		if err := runSettingsLs(c, nil); err != nil {
			t.Fatalf("settings ls: %v", err)
		}
	})
	for _, want := range []string{"KEY", "TYPE", "CHANGED", "work.draft.min_prompts", "strings", `line one\nline two`} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q:\n%s", want, out)
		}
	}
	// A modified key is flagged, an unchanged one is not.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "work.draft.min_prompts") && strings.Contains(line, "yes") {
			t.Errorf("unchanged key reported as changed: %q", line)
		}
		if strings.HasPrefix(line, "work.draft.exclude_projects") && !strings.Contains(line, "yes") {
			t.Errorf("modified key not flagged: %q", line)
		}
	}

	settingsLsOpts.jsonOut = true
	t.Cleanup(func() { settingsLsOpts.jsonOut = false })
	out = captureOutput(t, func() {
		if err := runSettingsLs(c, nil); err != nil {
			t.Fatalf("settings ls --json: %v", err)
		}
	})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("ls --json is not a JSON array: %v\n%s", err, out)
	}
	if len(got) != 3 || got[0]["key"] != "work.draft.min_prompts" {
		t.Fatalf("unexpected json entries: %+v", got)
	}
}

func TestSettingsGetPrintsValueAsWritten(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems()}
	stub.server(t)
	group := NewSettingsCmd()
	c := bindCmd(group.Subs[1]) // get

	c.Arg("key").WithValue("work.draft.exclude_projects")
	out := captureOutput(t, func() {
		if err := runSettingsGet(c, nil); err != nil {
			t.Fatalf("settings get list: %v", err)
		}
	})
	if out != "demo\nsandbox\n" {
		t.Fatalf("a list prints one item per line, got %q", out)
	}

	c = bindCmd(group.Subs[1])
	c.Arg("key").WithValue("work.draft.min_prompts")
	out = captureOutput(t, func() {
		if err := runSettingsGet(c, nil); err != nil {
			t.Fatalf("settings get int: %v", err)
		}
	})
	if out != "2\n" {
		t.Fatalf("an int prints as written, got %q", out)
	}

	settingsGetOpts.jsonOut = true
	c = bindCmd(group.Subs[1])
	// The bind resets the option vars to their declared defaults, so the flag value has
	// to be set after it (this is what --json does on the command line).
	settingsGetOpts.jsonOut = true
	t.Cleanup(func() { settingsGetOpts.jsonOut = false })
	c.Arg("key").WithValue("work.draft.min_prompts")
	out = captureOutput(t, func() {
		if err := runSettingsGet(c, nil); err != nil {
			t.Fatalf("settings get --json: %v", err)
		}
	})
	if !strings.Contains(out, `"kind": "int"`) || !strings.Contains(out, `"min": 1`) {
		t.Fatalf("--json should print the whole entry, got %s", out)
	}

	// An unregistered key is the server's 404, reported as a coded failure.
	c = bindCmd(group.Subs[1])
	c.Arg("key").WithValue("nope.key")
	err := runSettingsGet(c, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("unknown key should fail with the server's reason, got %v", err)
	}
	assertCodedExit(t, err)
}

func TestSettingsSetConvertsByKind(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems(), putItem: map[string]any{
		"key": "work.draft.min_prompts", "kind": "int", "value": 3, "modified": true,
	}}
	stub.server(t)
	group := NewSettingsCmd()

	c := bindCmd(group.Subs[2]) // set
	c.Arg("key").WithValue("work.draft.min_prompts")
	c.Arg("value").WithValue("3")
	out := captureOutput(t, func() {
		if err := runSettingsSet(c, []string{"work.draft.min_prompts", "3"}); err != nil {
			t.Fatalf("settings set int: %v", err)
		}
	})
	if !strings.Contains(out, "set work.draft.min_prompts = 3") {
		t.Fatalf("unexpected set output: %q", out)
	}
	// The value must reach the server as a NUMBER: a string would be refused by the
	// key's registered type.
	if body := stub.last().Body; !strings.Contains(body, `"value":3`) {
		t.Fatalf("int setting sent as non-number: %s", body)
	}

	// A list key keeps its items: comma-separated on the command line, an array on the wire.
	stub.items = settingsStubItems()
	c = bindCmd(group.Subs[2])
	c.Arg("key").WithValue("work.draft.exclude_projects")
	c.Arg("value").WithValue("demo, sandbox")
	if err := runSettingsSet(c, []string{"work.draft.exclude_projects", "demo, sandbox"}); err != nil {
		t.Fatalf("settings set list: %v", err)
	}
	if body := stub.last().Body; !strings.Contains(body, `["demo","sandbox"]`) {
		t.Fatalf("list setting sent as non-array: %s", body)
	}
}

func TestSettingsSetFromFileOrStdin(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems(), putItem: map[string]any{
		"key": "prompt.work.summarizer", "kind": "string", "value": "a\nb", "modified": true, "multiline": true,
	}}
	stub.server(t)
	group := NewSettingsCmd()

	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte("a\nb\n"), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}
	c := bindCmd(group.Subs[2])
	settingsSetOpts.file = path
	t.Cleanup(func() { settingsSetOpts.file = "" })
	c.Arg("key").WithValue("prompt.work.summarizer")
	out := captureOutput(t, func() {
		if err := runSettingsSet(c, []string{"prompt.work.summarizer"}); err != nil {
			t.Fatalf("settings set --file: %v", err)
		}
	})
	if !strings.Contains(out, "set prompt.work.summarizer (3 characters)") {
		t.Fatalf("a prompt write should report its size, not its text: %q", out)
	}
	// The file's trailing newline is not part of the prompt.
	if body := stub.last().Body; !strings.Contains(body, `"value":"a\nb"`) {
		t.Fatalf("file value not sent verbatim: %q", body)
	}

	// -f - reads stdin.
	stdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString("from stdin\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	w.Close()
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = stdin })
	c = bindCmd(group.Subs[2])
	settingsSetOpts.file = "-"
	c.Arg("key").WithValue("prompt.work.summarizer")
	if err := runSettingsSet(c, []string{"prompt.work.summarizer"}); err != nil {
		t.Fatalf("settings set --file -: %v", err)
	}
	if body := stub.last().Body; !strings.Contains(body, `"value":"from stdin"`) {
		t.Fatalf("stdin value not sent: %q", body)
	}

	// A value that does not match the key's kind is refused before the request.
	c = bindCmd(group.Subs[2])
	settingsSetOpts.file = ""
	c.Arg("key").WithValue("work.draft.min_prompts")
	c.Arg("value").WithValue("many")
	err = runSettingsSet(c, []string{"work.draft.min_prompts", "many"})
	if err == nil || !strings.Contains(err.Error(), "not an integer") {
		t.Fatalf("a non-numeric int value should be refused locally, got %v", err)
	}
}

func TestSettingsResetReportsDefault(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems()}
	stub.server(t)
	c := bindCmd(NewSettingsCmd().Subs[3]) // reset
	c.Arg("key").WithValue("work.draft.min_prompts")
	out := captureOutput(t, func() {
		if err := runSettingsReset(c, nil); err != nil {
			t.Fatalf("settings reset: %v", err)
		}
	})
	if !strings.Contains(out, "reset work.draft.min_prompts") || !strings.Contains(out, "default 2") {
		t.Fatalf("unexpected reset output: %q", out)
	}
	if req := stub.last(); req.Method != http.MethodDelete || req.Path != "/v1/settings/work.draft.min_prompts" {
		t.Fatalf("reset should DELETE the key, got %s %s", req.Method, req.Path)
	}
}

func TestSettingsExportWritesYAML(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems()}
	stub.server(t)
	c := bindCmd(NewSettingsCmd().Subs[4]) // export

	out := captureOutput(t, func() {
		if err := runSettingsExport(c, nil); err != nil {
			t.Fatalf("settings export: %v", err)
		}
	})
	if !strings.Contains(out, "work.draft.min_prompts: 3") {
		t.Fatalf("export should print the YAML verbatim, got %q", out)
	}

	path := filepath.Join(t.TempDir(), "settings.yaml")
	c = bindCmd(NewSettingsCmd().Subs[4])
	settingsExportOpts.out = path
	t.Cleanup(func() { settingsExportOpts.out = "" })
	captureOutput(t, func() {
		if err := runSettingsExport(c, nil); err != nil {
			t.Fatalf("settings export -o: %v", err)
		}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if !strings.Contains(string(data), "work.draft.min_prompts: 3") {
		t.Fatalf("exported file content: %q", data)
	}
}

func TestSettingsImportPreviewThenWrite(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems(), setImport: map[string]any{
		"dry_run": true, "applied": false, "errors": 0,
		"changes": []map[string]any{
			{"key": "work.draft.min_prompts", "action": "set", "old": 2, "new": 3},
			{"key": "work.draft.exclude_projects", "action": "unchanged", "old": []string{"demo"}, "new": []string{"demo"}},
			{"key": "work.draft.exclude_cwds", "action": "reset", "old": []string{"/tmp/x"}, "new": nil},
		},
	}}
	stub.server(t)
	group := NewSettingsCmd()

	path := filepath.Join(t.TempDir(), "import.yaml")
	if err := os.WriteFile(path, []byte("work.draft.min_prompts: 3\n"), 0o644); err != nil {
		t.Fatalf("write import file: %v", err)
	}
	c := bindCmd(group.Subs[5]) // import
	c.Arg("file").WithValue(path)
	out := captureOutput(t, func() {
		if err := runSettingsImport(c, []string{path}); err != nil {
			t.Fatalf("settings import preview: %v", err)
		}
	})
	// The preview sends dry_run, so nothing is written by default.
	if req := stub.last(); !strings.Contains(req.Query, "dry_run=1") {
		t.Fatalf("import without --yes must ask for a dry run, got query %q", req.Query)
	}
	for _, want := range []string{"set", "work.draft.min_prompts", "2 → 3", "unchanged", "reset", "(default)", "nothing written", "--yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview output missing %q:\n%s", want, out)
		}
	}

	// --yes writes.
	settingsImportOpts.yes = true
	t.Cleanup(func() { settingsImportOpts.yes = false })
	c = bindCmd(group.Subs[5])
	settingsImportOpts.yes = true
	c.Arg("file").WithValue(path)
	captureOutput(t, func() {
		if err := runSettingsImport(c, []string{path}); err != nil {
			t.Fatalf("settings import --yes: %v", err)
		}
	})
	if req := stub.last(); strings.Contains(req.Query, "dry_run") {
		t.Fatalf("import --yes must not ask for a dry run, got query %q", req.Query)
	}
	if req := stub.last(); req.ContentType != "application/yaml" {
		t.Fatalf("import should post YAML, got content type %q", req.ContentType)
	}
}

func TestSettingsImportReportsRefusedKeys(t *testing.T) {
	stub := &settingsStub{items: settingsStubItems(), setImport: map[string]any{
		"dry_run": true, "applied": false, "errors": 1,
		"changes": []map[string]any{
			{"key": "work.draft.min_prompts", "action": "set", "old": 2, "new": 99, "error": "99 is above the maximum 20"},
		},
	}}
	stub.server(t)
	path := filepath.Join(t.TempDir(), "import.yaml")
	if err := os.WriteFile(path, []byte("work.draft.min_prompts: 99\n"), 0o644); err != nil {
		t.Fatalf("write import file: %v", err)
	}
	c := bindCmd(NewSettingsCmd().Subs[5])
	c.Arg("file").WithValue(path)
	var err error
	out := captureOutput(t, func() { err = runSettingsImport(c, []string{path}) })
	if !strings.Contains(out, "above the maximum 20") {
		t.Fatalf("the refusal reason should be shown: %q", out)
	}
	if err == nil {
		t.Fatal("a refused key must exit non-zero")
	}
	assertCodedExit(t, err)
}

// bindToolCmd builds the `tool api` command with its options bound.
func bindToolApi(t *testing.T) *gcli.Command {
	t.Helper()
	def := NewToolApiCmd()
	c := gcli.NewCommand(def.Name, def.Desc, nil)
	def.Config(c)
	return c
}
