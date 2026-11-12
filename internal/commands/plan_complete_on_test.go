package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/client"
)

func TestCheckCompleteOnFlagAndWords(t *testing.T) {
	for _, ok := range []string{"", "job", "merge", " merge "} {
		if err := checkCompleteOnFlag(ok); err != nil {
			t.Fatalf("checkCompleteOnFlag(%q) = %v", ok, err)
		}
	}
	if err := checkCompleteOnFlag("later"); err == nil || !strings.Contains(err.Error(), "--complete-on") {
		t.Fatalf("checkCompleteOnFlag(later) = %v, want an error naming the flag", err)
	}
	if got := effectivePlanCompleteOnWord(""); got != "job" {
		t.Fatalf("empty complete_on reads %q, want job", got)
	}
	lines := planSetChanges(client.Plan{CompleteOn: "merge"}, false, false, true, false, false)
	if len(lines) != 1 || lines[0] != "complete_on -> merge" {
		t.Fatalf("planSetChanges = %v", lines)
	}
}
