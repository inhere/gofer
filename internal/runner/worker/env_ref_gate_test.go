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
	if got := unsupportedDispatchFields(wsproto.ReadDirsMinProtocolVersion-1, &runner.Forward{ReadDirs: []string{"docs"}}); len(got) != 1 || got[0] != "read_dirs" {
		t.Fatalf("lacks = %v, want [read_dirs]", got)
	}
	if got := unsupportedDispatchFields(wsproto.ReadDirsMinProtocolVersion, &runner.Forward{ReadDirs: []string{"docs"}}); len(got) != 0 {
		t.Fatalf("lacks = %v, want none at v%d", got, wsproto.ReadDirsMinProtocolVersion)
	}
	if got := unsupportedDispatchFields(2, &runner.Forward{}); len(got) != 0 {
		t.Fatalf("lacks = %v, want none for a plain job", got)
	}
}

// TestDispatchPermissionTierProtocolGate: a worker below v31 would drop the tier and run the
// job under its own, possibly looser, approval gate, so it is REFUSED with the capability
// named; v31 carries it; a job without a tier is never refused.
func TestDispatchPermissionTierProtocolGate(t *testing.T) {
	f := &runner.Forward{PermissionTier: "ask"}
	if got := unsupportedDispatchFields(wsproto.PermissionTierMinProtocolVersion-1, f); len(got) != 1 || got[0] != "permission_tier" {
		t.Fatalf("lacks = %v, want [permission_tier] for an older worker", got)
	}
	if got := unsupportedDispatchFields(wsproto.PermissionTierMinProtocolVersion, f); len(got) != 0 {
		t.Fatalf("lacks = %v, want none at v%d", got, wsproto.PermissionTierMinProtocolVersion)
	}
	if got := unsupportedDispatchFields(wsproto.PermissionTierMinProtocolVersion-1, &runner.Forward{}); len(got) != 0 {
		t.Fatalf("lacks = %v, want none without a tier", got)
	}
}
