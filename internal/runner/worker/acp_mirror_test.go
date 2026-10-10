package worker

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestBoundedSinkMirrorsACPStream (gofer-e2x7): an "acp" log frame is appended to the
// HOST job's artifacts/acp.jsonl verbatim — never to stdout (an older hub's mistake
// that the protocol gate exists to avoid), never truncated with a marker, and it
// does not move the stdout/stderr resume offsets.
func TestBoundedSinkMirrorsACPStream(t *testing.T) {
	old := maxWSFrameBytes
	maxWSFrameBytes = 16
	defer func() { maxWSFrameBytes = old }()

	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	s := newBoundedSink(&stdout, &stderr, nil)
	s.resultDir = dir

	line1 := `{"t":"prompt","text":"` + strings.Repeat("p", 40) + `"}` + "\n"
	line2 := `{"t":"message","text":"hi"}` + "\n"
	s.WriteLog(wsproto.LogStreamACP, 1, line1)
	s.WriteLog(wsproto.LogStreamACP, 2, line2)
	s.WriteLog("stdout", 3, "plain")

	got, err := os.ReadFile(runner.ACPArtifactPath(dir))
	if err != nil {
		t.Fatalf("read mirrored acp.jsonl: %v", err)
	}
	if string(got) != line1+line2 {
		t.Fatalf("mirrored acp.jsonl = %q, want the two lines verbatim", got)
	}
	if stdout.String() != "plain" || stderr.Len() != 0 {
		t.Fatalf("acp frames leaked into stdio: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if out, errOut := s.Suspend("test"); out != int64(len("plain")) || errOut != 0 {
		t.Fatalf("resume offsets = %d/%d, want only the stdout bytes counted", out, errOut)
	}
}

// TestBoundedSinkDropsACPWithoutResultDir: a sink with no host result dir (a peer
// or test wiring) drops the stream instead of writing it anywhere.
func TestBoundedSinkDropsACPWithoutResultDir(t *testing.T) {
	var stdout bytes.Buffer
	s := newBoundedSink(&stdout, &stdout, nil)
	s.WriteLog(wsproto.LogStreamACP, 1, `{"t":"message"}`+"\n")
	if stdout.Len() != 0 {
		t.Fatalf("acp frame without a result dir reached stdout: %q", stdout.String())
	}
}
