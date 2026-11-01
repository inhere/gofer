package hookrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// TestTruncateMarkerFollowsLanguage: the marker a cut-off relayed message ends with is in the
// language of the session (zh-CN verbatim what it was; English has no CJK).
func TestTruncateMarkerFollowsLanguage(t *testing.T) {
	long := strings.Repeat("a", 200)
	if got := truncate(i18n.ZhCN, long, 100); !strings.HasSuffix(got, "\n[已截断]") || len(got) > 100 {
		t.Errorf("zh = %q", got[len(got)-20:])
	}
	if got := truncate(i18n.EN, long, 100); !strings.HasSuffix(got, "\n[truncated]") || len(got) > 100 {
		t.Errorf("en = %q", got[len(got)-20:])
	}
	if got := truncate(i18n.EN, "short", 100); got != "short" {
		t.Errorf("short text changed: %q", got)
	}
}
