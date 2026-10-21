package worker

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestDispatchEnvAndRefProtocolGate pins the v26 refusal (design 一·3 / 一·4): a job that
// carries env or a ref commit needs the dispatch fields, so a worker below
// JobEnvMinProtocolVersion is REFUSED (with the capability named) instead of being handed
// a job that would run without its variables or in the wrong tree.
func TestDispatchEnvAndRefProtocolGate(t *testing.T) {
	f := &runner.Forward{Env: map[string]string{"GOOS": "darwin"}, Ref: "0123456789abcdef"}
	lacks := unsupportedDispatchFields(wsproto.JobEnvMinProtocolVersion-1, f)
	joined := strings.Join(lacks, ",")
	if !strings.Contains(joined, "env") || !strings.Contains(joined, "ref") {
		t.Fatalf("lacks = %v, want both env and ref named for a v25 worker", lacks)
	}
	if got := unsupportedDispatchFields(wsproto.JobEnvMinProtocolVersion, f); len(got) != 0 {
		t.Fatalf("lacks = %v, want none at v%d", got, wsproto.JobEnvMinProtocolVersion)
	}
	// An absent field is never a reason to refuse: an ordinary job still runs anywhere.
	if got := unsupportedDispatchFields(2, &runner.Forward{}); len(got) != 0 {
		t.Fatalf("lacks = %v, want none for a plain job", got)
	}
}
