package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteStepResult: a built-in step's result is written as result.json under
// $GOFER_RESULT_DIR, which is what makes ${steps.<step>.result} work for the next step.
func TestWriteStepResult(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOFER_RESULT_DIR", dir)
	writeStepResult(map[string]any{"ok": true, "pushed": []string{"main"}})
	b, err := os.ReadFile(filepath.Join(dir, "result.json"))
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("result.json is not JSON: %v", err)
	}
	if got["ok"] != true {
		t.Fatalf("result.json = %s", b)
	}

	// Without the directory (a hand-run `gofer tool step`) nothing is written and nothing
	// fails: the stdout line is the result then.
	t.Setenv("GOFER_RESULT_DIR", "")
	writeStepResult(map[string]any{"ok": true})
}
