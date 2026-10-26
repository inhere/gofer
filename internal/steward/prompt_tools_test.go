package steward

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/mcptools"
)

// goferToolRef matches a gofer tool name inside prompt text ("gofer_work_remind"). The
// second group keeps a name from ending on a trailing underscore.
var goferToolRef = regexp.MustCompile(`gofer_[a-z0-9]+(?:_[a-z0-9]+)*`)

// TestPromptsOnlyNameAllowedGoferTools guards the other half of gofer-j0aq: the steward's
// prompts tell it to call gofer tools, and every one of them must be a tool the steward can
// run without a human — a read-only tool, an advice-only write, or one of
// mcptools.StewardTools. Anything else (a job submit, a memory delete) is refused by the
// steward's MCP surface, and a name nobody classifies is the bug this task fixed: the
// session parked on a human for every call and the turn ended as a refusal. So naming a new
// tool in a prompt is a deliberate change: classify it in internal/mcptools first.
//
// The scan reads the package's own sources, which is where every prompt lives (prime.go /
// review.go / today.go / memory.go / noise.go / notes.go), so a new prompt file is covered
// without touching this test.
func TestPromptsOnlyNameAllowedGoferTools(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	seen := map[string]string{} // tool name -> the file that named it
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, tool := range goferToolRef.FindAllString(string(b), -1) {
			if _, ok := seen[tool]; !ok {
				seen[tool] = name
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no gofer tool name found in the steward prompts: the scan is broken")
	}
	var bad []string
	for tool, file := range seen {
		if mcptools.IsReadOnly(tool) || mcptools.IsAdvisory(tool) || mcptools.IsStewardTool(tool) {
			continue
		}
		bad = append(bad, file+": "+tool)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("these gofer tools are named in the steward prompts but are neither read-only / advice-only nor one of mcptools.StewardTools (%v):\n  %s",
			mcptools.StewardTools(), strings.Join(bad, "\n  "))
	}
}
