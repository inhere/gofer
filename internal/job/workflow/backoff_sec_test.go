package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorkflowSpecBackoffSecBothSpellings pins gofer-pn0x on the WORKFLOW path: a step's
// `retry.backoff_sec` reads both a single number and a list, in a workflow yaml and in an
// exported json spec, and both mean the same table.
func TestWorkflowSpecBackoffSecBothSpellings(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"scalar.yaml": `title: scalar
steps:
  - name: only
    agent: exec
    prompt: go
    on_failure: retry
    retry:
      max_attempts: 3
      backoff_sec: 300
`,
		"list.yaml": `title: list
steps:
  - name: only
    agent: exec
    prompt: go
    on_failure: retry
    retry:
      max_attempts: 3
      backoff_sec: [300]
`,
		"scalar.json": `{"title":"scalar","steps":[{"name":"only","agent":"exec","prompt":"go",
  "on_failure":"retry","retry":{"max_attempts":3,"backoff_sec":300}}]}`,
		"list.json": `{"title":"list","steps":[{"name":"only","agent":"exec","prompt":"go",
  "on_failure":"retry","retry":{"max_attempts":3,"backoff_sec":[300]}}]}`,
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		spec, err := ParseWorkflowFile(path)
		if err != nil {
			t.Fatalf("%s: ParseWorkflowFile: %v", name, err)
		}
		retry := spec.Steps[0].Retry
		if retry == nil || len(retry.BackoffSec) != 1 || retry.BackoffSec[0] != 300 {
			t.Fatalf("%s: retry = %+v, want backoff_sec [300]", name, retry)
		}
	}
}
