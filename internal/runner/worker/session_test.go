package worker

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

func TestRemoteSessionDispatchRejectedForOldWorker(t *testing.T) {
	f := &runner.Forward{Session: true, IdleTimeoutSec: 30, MaxSessionSec: 120}
	for _, proto := range []int{0, wsproto.SessionJobMinProtocolVersion - 1} {
		lacks := unsupportedDispatchFields(proto, f)
		if len(lacks) == 0 || !strings.Contains(strings.Join(lacks, ","), "session") {
			t.Fatalf("protocol v%d lacks = %v, want session capability", proto, lacks)
		}
	}
	if got := unsupportedDispatchFields(wsproto.SessionJobMinProtocolVersion, &runner.Forward{}); len(got) != 0 {
		t.Fatalf("ordinary dispatch on v13 rejected: %v", got)
	}
}
