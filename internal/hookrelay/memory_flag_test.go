package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/tracker"
)

// A flagged memory hit by a keyword is still injected in full, behind the 待复核 prefix.
func TestPromptMemoryFlagPrefix(t *testing.T) {
	m := tracker.Memory{Key: "release", Content: "RELEASE STEPS", UpdatedAt: "2026-10-01T00:00:00Z",
		MemoryMeta: tracker.MemoryMeta{Kind: tracker.MemoryKindRule, When: &tracker.MemoryWhen{Keywords: []string{"发版"}},
			Flags: []tracker.MemoryFlag{{At: "2026-10-09T00:00:00Z", Reason: "tag 规则已变"}}}}
	hits := matchPromptMemories([]PromptMemory{{Memory: m}}, "准备发版", "", time.Now(), nil)
	out := renderPromptMemories(hits, 0)
	if !strings.Contains(out, "⚠ 待复核（tag 规则已变） release\nRELEASE STEPS") {
		t.Fatalf("flag prefix missing: %q", out)
	}
}
