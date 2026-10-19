package configlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

const lintSource = `server:
  token: t
  web_enabled: true
log:
  max_size_mb: 50
agents:
  tool:
    type: cli-agent
    command: tool
    args: [run, "{{prompt}}"]
    detect:
      command: tool
      args: [--version]
  exec:
    type: exec
  claude:
    type: cli-agent
    command: claude
    args: [-p, --output-format, stream-json, --verbose, "{{prompt}}"]
    interactive_args: []
    global_args: []
    output_format: ndjson
    rules: [house]
projects:
  p:
    host_path: /x
    allowed_runners: [local]
    exchange_subdir: tmp
    hooks:
      - name: keep-me
        exchange_subdir: tmp
`

func writeConfig(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "config.yaml")
}

func findingPaths(r Report, kind string) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range r.Findings {
		if f.Kind == kind {
			out[f.Path] = f
		}
	}
	return out
}

func TestLintReportsOnlyKeysThatChangeNothing(t *testing.T) {
	main := writeConfig(t, map[string]string{"config.yaml": lintSource})
	r, err := Lint(main, TargetServer)
	if err != nil {
		t.Fatal(err)
	}
	got := findingPaths(r, KindRemove)
	for _, want := range []string{"server.web_enabled", "log.max_size_mb", "agents.tool.detect", "agents.exec", "projects.p.exchange_subdir"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s not reported as removable; got %v", want, keys(got))
		}
	}
	for _, keep := range []string{"server.token", "agents.tool.args", "agents.tool.command", "projects.p.host_path", "agents.claude.rules"} {
		if _, ok := got[keep]; ok {
			t.Errorf("%s reported removable, but deleting it changes the config", keep)
		}
	}
	if f := got["agents.tool.detect"]; f.Line != 11 || f.Lines != 3 || !strings.Contains(f.Reason, "version probe") {
		t.Errorf("detect finding = %+v, want line 11, 3 lines, probe reason", f)
	}
	// agents.exec is removable as a whole: its children are not listed again.
	if _, ok := got["agents.exec.type"]; ok {
		t.Error("children of a removable entry are reported again")
	}
	if u, ok := findingPaths(r, KindUnknown)["projects.p.hooks"]; !ok || !strings.Contains(u.Reason, "unknown key") {
		t.Errorf("projects.p.hooks not reported as unknown; findings %+v", r.Findings)
	}
	if _, ok := got["projects.p.hooks"]; ok {
		t.Error("an unknown key must not be offered for removal")
	}
	hint, ok := findingPaths(r, KindExtends)["agents.claude"]
	if !ok {
		t.Fatalf("no extends hint for claude; findings %+v", r.Findings)
	}
	if hint.Suggestion != "  claude:\n    extends: builtin\n    rules: [house]" {
		t.Fatalf("suggestion:\n%s", hint.Suggestion)
	}
}

func TestFixRemovesAndStaysEquivalent(t *testing.T) {
	main := writeConfig(t, map[string]string{"config.yaml": lintSource})
	before, _, err := config.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := effective(before)
	res, err := Fix(main, TargetServer)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) < 5 || len(res.Backups) != 1 {
		t.Fatalf("fix result %+v", res)
	}
	text, _ := os.ReadFile(main)
	s := string(text)
	for _, gone := range []string{"web_enabled", "log:", "detect:", "exec:", "allowed_runners: [local]\n    exchange_subdir: tmp"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q still in the fixed file:\n%s", gone, s)
		}
	}
	if !strings.Contains(s, "    hooks:\n      - name: keep-me\n        exchange_subdir: tmp") {
		t.Errorf("the unknown hooks block (and the list items in it) must be left alone:\n%s", s)
	}
	if b, _ := os.ReadFile(res.Backups[0]); string(b) != lintSource {
		t.Error("backup is not the original")
	}
	after, _, err := config.Load(main)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := effective(after); got != want {
		t.Fatal("fixed config is not equivalent")
	}
	// Nothing left: a second fix is a no-op.
	if res, err := Fix(main, TargetServer); err != nil || len(res.Removed) != 0 {
		t.Fatalf("second fix = %+v, %v", res, err)
	}
}

func TestLintCoversIncludedFiles(t *testing.T) {
	main := writeConfig(t, map[string]string{
		"config.yaml":          "include: [config.d/*.yaml]\nserver:\n  token: t\n",
		"config.d/agents.yaml": "agents:\n  tool:\n    type: cli-agent\n    command: tool\n    args: [\"{{prompt}}\"]\n    detect:\n      command: tool\n      args: [--version]\n",
	})
	r, err := Lint(main, TargetServer)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := findingPaths(r, KindRemove)["agents.tool.detect"]
	if !ok || !strings.HasSuffix(filepath.ToSlash(f.File), "config.d/agents.yaml") || f.Line != 6 {
		t.Fatalf("finding = %+v (ok=%v), want it in config.d/agents.yaml line 6", f, ok)
	}
	if _, err := Fix(main, TargetServer); err != nil {
		t.Fatal(err)
	}
	frag, _ := os.ReadFile(filepath.Join(filepath.Dir(main), "config.d/agents.yaml"))
	if strings.Contains(string(frag), "detect") {
		t.Fatalf("fragment not fixed:\n%s", frag)
	}
}

func TestScanNodes(t *testing.T) {
	src := `a:
  # about b
  b: 1
  list:
  - x
  - y
  items:
    - name: n
      deep: 2
  text: |
    k: not a key
  c:
    d: [1, 2]

e: 3
`
	nodes := scanNodes(strings.Split(src, "\n"))
	byPath := map[string]node{}
	for _, n := range nodes {
		byPath[n.path] = n
	}
	for _, p := range []string{"a", "a.b", "a.list", "a.items", "a.text", "a.c", "a.c.d", "e"} {
		if _, ok := byPath[p]; !ok {
			t.Errorf("missing %s", p)
		}
	}
	for _, p := range []string{"a.items.name", "a.items.deep", "a.text.k", "a.name", "a.deep", "a.k"} {
		if _, ok := byPath[p]; ok {
			t.Errorf("%s must not be listed (inside a list item or block scalar)", p)
		}
	}
	if b := byPath["a.b"]; b.start != 1 || b.end != 3 {
		t.Errorf("a.b range = [%d,%d), want [1,3) with its head comment", b.start, b.end)
	}
	if l := byPath["a.list"]; l.end != 6 {
		t.Errorf("a.list end = %d, want 6 (list items at the key's indent)", l.end)
	}
	if a := byPath["a"]; a.end != 13 {
		t.Errorf("a end = %d, want 13 (trailing blank line excluded)", a.end)
	}
}

func keys(m map[string]Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

const workerLintSource = `worker_id: w1
server_link:
  addr: http://127.0.0.1:1
agents:
  tool:
    type: cli-agent
    command: tool
    args: [run, "{{prompt}}"]
    detect:
      command: tool
      args: [--version]
  exec:
    type: exec
roots:
  - path: /x
gaurds: {}
`

// A worker.yaml is linted with the worker model: agent keys the runtime would fill in
// anyway are removable, a typo is reported as unknown, and --fix keeps the file
// loading as a worker config.
func TestLintWorkerConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "worker.yaml")
	if err := os.WriteFile(p, []byte(workerLintSource), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Lint(p, TargetWorker)
	if err != nil {
		t.Fatal(err)
	}
	removable := findingPaths(r, KindRemove)
	for _, want := range []string{"agents.tool.detect", "agents.exec"} {
		if _, ok := removable[want]; !ok {
			t.Errorf("%s not reported removable: %+v", want, r.Findings)
		}
	}
	for _, keep := range []string{"worker_id", "roots", "agents.tool.args", "server_link"} {
		if _, ok := removable[keep]; ok {
			t.Errorf("%s reported removable", keep)
		}
	}
	if _, ok := findingPaths(r, KindUnknown)["gaurds"]; !ok {
		t.Errorf("typo gaurds not reported unknown: %+v", r.Findings)
	}
	res, err := Fix(p, TargetWorker)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) < 2 {
		t.Fatalf("removed %+v", res.Removed)
	}
	b, _ := os.ReadFile(p)
	wc, err := config.DecodeWorkerConfig(p, b)
	if err != nil {
		t.Fatalf("fixed worker.yaml does not load: %v\n%s", err, b)
	}
	if wc.Agents["tool"].Command != "tool" || strings.Contains(string(b), "detect:") || !strings.Contains(string(b), "gaurds") {
		t.Fatalf("unexpected fixed file:\n%s", b)
	}
}

// The attachment keys are part of the model, so lint knows them (and still flags a typo).
func TestLintKnowsAttachmentKeys(t *testing.T) {
	main := writeConfig(t, map[string]string{"config.yaml": `server:
  token: t
  attachments:
    max_bytes: 1048576
    max_per_message: 6
    hourly_per_caller: 200
    max_byte: 1
storage:
  attachment_retention_days: 14
`})
	r, err := Lint(main, TargetServer)
	if err != nil {
		t.Fatal(err)
	}
	unknown := findingPaths(r, KindUnknown)
	if _, ok := unknown["server.attachments.max_byte"]; !ok {
		t.Errorf("the typo is not reported as unknown: %+v", r.Findings)
	}
	for _, known := range []string{"server.attachments.max_bytes", "server.attachments.max_per_message", "server.attachments.hourly_per_caller", "storage.attachment_retention_days"} {
		if _, ok := unknown[known]; ok {
			t.Errorf("%s reported unknown", known)
		}
	}
}
