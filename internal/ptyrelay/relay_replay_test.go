package ptyrelay

import (
	"bytes"
	"testing"
	"time"
)

// These two tests pin the attach invariant behind h-aii-rx9a (web pty flicker /
// repeated line redraws): a viewer that attaches (or RE-attaches) while the pty is
// mid-stream must receive every recorded byte exactly ONCE — the pre-attach
// scrollback replayed at registration, then the live tail — and never a byte in
// both.
//
// The pre-fix transport registered the viewer and THEN snapshotted the ring, so
// every chunk the recorder appended inside that window went out twice (measured on
// the pre-fix code with tmp/replaydiag: 306 of 816 bytes over-delivered, i.e. the
// three chunks the pty emitted during the attach appeared twice on screen).

// TestRingReplayedOncePerViewer: the scrollback a viewer attaches with is the
// history recorded BEFORE its registration, delivered once; output produced after
// registration arrives once, on the viewer's stream.
func TestRingReplayedOncePerViewer(t *testing.T) {
	src := newFakeSource()
	r := New(src, WithRingSize(1<<20))
	r.Start()
	defer r.Close()

	hist := []byte("HISTORY-BEFORE-ATTACH-0001:")
	src.Emit(hist)
	waitFor(t, 2*time.Second, func() bool { return r.RecordedLen() >= len(hist) })

	v, err := r.AddViewer(false)
	if err != nil {
		t.Fatalf("AddViewer: %v", err)
	}
	defer v.Close()

	// The pty keeps streaming while the transport is still setting the viewer up.
	inflight := []byte("IN-FLIGHT-DURING-ATTACH-0002:")
	src.Emit(inflight)
	waitFor(t, 2*time.Second, func() bool { return r.RecordedLen() >= len(hist)+len(inflight) })

	replay := v.Replay()
	if !bytes.Contains(replay, hist) {
		t.Fatalf("replay missing pre-attach history: got %q", replay)
	}

	live := []byte("LIVE-AFTER-ATTACH-0003:")
	src.Emit(live)
	got := readViewer(t, v, len(live), time.Second)
	if !bytes.Contains(got, live) {
		t.Fatalf("viewer missing live output: got %q", got)
	}

	delivered := append(append([]byte{}, replay...), got...)
	for _, marker := range [][]byte{hist, inflight, live} {
		if n := bytes.Count(delivered, marker); n != 1 {
			t.Fatalf("marker %q delivered %d times, want exactly 1 (replay=%q stream=%q)",
				marker, n, replay, got)
		}
	}
	if len(delivered) != r.RecordedLen() {
		t.Fatalf("viewer received %d bytes, recorder took %d", len(delivered), r.RecordedLen())
	}
}

// TestReconnectDoesNotDuplicateRing: dropping a viewer and attaching again replays
// the CURRENT ring once (not the ring it already saw once more), and the bytes
// recorded while nobody was attached are replayed exactly once — a reattaching
// browser ends up with one copy of the screen, not two.
func TestReconnectDoesNotDuplicateRing(t *testing.T) {
	src := newFakeSource()
	r := New(src, WithRingSize(1<<20))
	r.Start()
	defer r.Close()

	first := []byte("FIRST-EPOCH-0001:")
	src.Emit(first)
	waitFor(t, 2*time.Second, func() bool { return r.RecordedLen() >= len(first) })

	v1, err := r.AddViewer(true)
	if err != nil {
		t.Fatalf("AddViewer: %v", err)
	}
	if replay1 := v1.Replay(); !bytes.Contains(replay1, first) {
		t.Fatalf("first viewer replay missing %q", first)
	}
	watched := []byte("WATCHED-LIVE-0002:")
	src.Emit(watched)
	_ = readViewer(t, v1, len(watched), time.Second)
	v1.Close()

	// Recorded while NO viewer is attached: only the reattach replay may carry it.
	unwatched := []byte("UNWATCHED-GAP-0003:")
	src.Emit(unwatched)
	waitFor(t, 2*time.Second, func() bool { return r.RecordedLen() >= len(first)+len(watched)+len(unwatched) })

	v2, err := r.AddViewer(false)
	if err != nil {
		t.Fatalf("AddViewer: %v", err)
	}
	defer v2.Close()
	replay2 := v2.Replay()
	for _, marker := range [][]byte{first, watched, unwatched} {
		if n := bytes.Count(replay2, marker); n != 1 {
			t.Fatalf("replay after reattach contains %q %d times, want exactly 1: %q", marker, n)
		}
	}
	if len(replay2) != r.RecordedLen() {
		t.Fatalf("replay is %d bytes, ring holds %d", len(replay2), r.RecordedLen())
	}

	tail := []byte("TAIL-AFTER-REATTACH-0004:")
	src.Emit(tail)
	got := readViewer(t, v2, len(tail), time.Second)
	if n := bytes.Count(got, tail); n != 1 {
		t.Fatalf("post-reattach tail delivered %d times, want exactly 1: %q", n, got)
	}
}
