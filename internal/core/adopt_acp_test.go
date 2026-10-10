package core

import (
	"os"
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestAdoptSinkMirrorsACPStream (gofer-e2x7): after a serve restart an adopted worker
// job keeps mirroring its structured record — the "acp" stream lands in the host
// result dir and never reaches the adopted job's stdio writers (the nil job handle
// would panic if it were touched).
func TestAdoptSinkMirrorsACPStream(t *testing.T) {
	dir := t.TempDir()
	s := &adoptSink{workerID: "w1", resultDir: dir}
	line := `{"t":"message","text":"hi"}` + "\n"
	s.WriteLog(wsproto.LogStreamACP, 1, line)
	got, err := os.ReadFile(runner.ACPArtifactPath(dir))
	if err != nil || string(got) != line {
		t.Fatalf("adopted acp mirror = %q (err %v), want %q", got, err, line)
	}
}
