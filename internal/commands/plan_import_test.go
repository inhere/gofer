package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

const importPlanText = "方案正文\n\n```gofer-todos\n" +
	"- title: store\n" +
	"  acceptance: [migrates]\n" +
	"  scope: internal/store/**\n" +
	"- title: api\n" +
	"  check: go test './internal/...'\n" +
	"- title: web\n" +
	"```\n"

// TestPlanImport (gofer-3nxa.5): --dry-run prints the chain without a server; a real
// import creates the items in order, wiring `after` to the ids just created and turning
// a check into an exec item with its argv; --from-job reads the planner's report.
func TestPlanImport(t *testing.T) {
	type added struct {
		Title    string    `json:"title"`
		Assignee *string   `json:"assignee"`
		After    *[]string `json:"after"`
		Cmd      *[]string `json:"cmd"`
		Scope    *[]string `json:"scope"`
		Accept   *string   `json:"acceptance"`
	}
	var adds []added
	var logReads int
	ts := isolateTodoCLI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/logs/stdout") {
			logReads++
			_, _ = w.Write([]byte(importPlanText))
			return
		}
		var a added
		_ = json.NewDecoder(r.Body).Decode(&a)
		adds = append(adds, a)
		_ = json.NewEncoder(w).Encode(client.Todo{TodoID: fmt.Sprintf("todo-%d", len(adds)), Title: a.Title})
	})
	reset := func() { planImportOpts = planImportFlags{} }
	reset()
	t.Cleanup(reset)
	file := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(file, []byte(importPlanText), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"plan", "import", "--server", "http://127.0.0.1:1", "plan-1", "-f", file, "--assign", "codex", "--dry-run"}); code != 0 {
			t.Fatalf("dry-run exit=%d", code)
		}
	})
	for _, want := range []string{"4 todo(s) from 3 step(s)", "#1 store  [codex]", "#3 检查点：api  [exec]  after #2", "cmd: go test './internal/...'", "#4 web  [codex]  after #3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if len(adds) != 0 {
		t.Fatalf("dry-run created todos: %+v", adds)
	}

	reset()
	if code := NewApp("test").Run([]string{"plan", "import", "--server", ts.URL, "plan-1", "--from-job", "job-p", "--assign", "codex"}); code != 0 {
		t.Fatalf("import exit=%d", code)
	}
	if logReads != 1 || len(adds) != 4 {
		t.Fatalf("logReads=%d adds=%+v", logReads, adds)
	}
	if adds[0].After != nil || *adds[0].Assignee != "codex" || *adds[0].Accept != "- migrates" || (*adds[0].Scope)[0] != "internal/store/**" {
		t.Fatalf("first item = %+v", adds[0])
	}
	if *adds[2].Assignee != "exec" || strings.Join(*adds[2].Cmd, " ") != "go test ./internal/..." || (*adds[2].After)[0] != "todo-2" {
		t.Fatalf("check item = %+v", adds[2])
	}
	if (*adds[3].After)[0] != "todo-3" {
		t.Fatalf("step after the check waits for todo-3: %+v", adds[3])
	}

	reset()
	if code := NewApp("test").Run([]string{"plan", "import", "--server", ts.URL, "plan-1"}); code == 0 {
		t.Fatalf("import without a source must fail")
	}
}
