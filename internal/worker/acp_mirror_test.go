package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestACPMirrorChunkKeepsLinesWhole pins the framing of the "acp" stream: a frame
// ends after the last complete line, a trailing partial line waits for its newline,
// and only a line longer than the frame budget is split.
func TestACPMirrorChunkKeepsLinesWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.jsonl")
	if err := os.WriteFile(path, []byte("ab\ncdef\ngh"), 0o644); err != nil {
		t.Fatal(err)
	}
	type step struct {
		chunk string
		next  int64
	}
	var got []step
	off := int64(0)
	for range 10 {
		chunk, next := acpMirrorChunk(path, off, 4)
		if len(chunk) == 0 {
			break
		}
		got = append(got, step{string(chunk), next})
		off = next
	}
	want := []step{{"ab\n", 3}, {"cdef", 7}, {"\n", 8}}
	if len(got) != len(want) {
		t.Fatalf("chunks = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunk %d = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	if off != 8 {
		t.Fatalf("partial trailing line was sent: offset %d, want 8", off)
	}
	if chunk, next := acpMirrorChunk(filepath.Join(t.TempDir(), "missing"), 5, 4); chunk != nil || next != 5 {
		t.Fatalf("missing file = %q/%d, want nothing at the same offset", chunk, next)
	}
}

// TestPumpACPMirrorGatedOnServerProtocol: the worker sends the "acp" stream only to a
// hub that registered with protocol v22+ — an older hub writes every non-stderr log
// frame into the job's stdout.
func TestPumpACPMirrorGatedOnServerProtocol(t *testing.T) {
	cl, frames, _ := dialLiveClient(t, &stubJobs{})
	path := filepath.Join(t.TempDir(), "acp.jsonl")
	if err := os.WriteFile(path, []byte(`{"t":"prompt","text":"hi"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cl.inflightCreate("r1")
	ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
	defer cancel()

	cl.serverProto.Store(int64(wsproto.ACPMirrorMinProtocolVersion - 1))
	cl.pumpACPMirror(ctx, path, "r1")
	if off := cl.inflightOffset("r1", wsproto.LogStreamACP); off != 0 {
		t.Fatalf("a v%d hub was sent the acp stream (offset %d)", wsproto.ACPMirrorMinProtocolVersion-1, off)
	}

	cl.serverProto.Store(int64(wsproto.ACPMirrorMinProtocolVersion))
	cl.pumpACPMirror(ctx, path, "r1")
	select {
	case env := <-frames:
		lf, err := wsproto.As[wsproto.Log](env)
		if err != nil || env.Type != wsproto.TypeLog || lf.Stream != wsproto.LogStreamACP || lf.Text != `{"t":"prompt","text":"hi"}`+"\n" {
			t.Fatalf("frame = %s %+v (err %v), want one acp log frame with the line", env.Type, lf, err)
		}
	case <-ctx.Done():
		t.Fatal("no acp frame reached a v22 hub")
	}
	if off := cl.inflightOffset("r1", wsproto.LogStreamACP); off == 0 {
		t.Fatal("acp offset did not advance after a successful frame")
	}
	if off := cl.inflightOffset("r1", "stdout"); off != 0 {
		t.Fatalf("acp frame moved the stdout offset to %d", off)
	}
}
