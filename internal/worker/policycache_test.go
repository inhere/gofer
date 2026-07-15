package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/wsproto"
)

// newPolicyClientCache is newPolicyClient with a real on-disk last-known-good cache at
// cachePath, so the cache read/write/invalidate paths are exercised end to end.
func newPolicyClientCache(t *testing.T, seam ReloadFunc, cachePath string) (*Client, chan wsproto.Envelope) {
	t.Helper()
	frames := make(chan wsproto.Envelope, 64)
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		for {
			var env wsproto.Envelope
			if rerr := wsjson.Read(req.Context(), conn, &env); rerr != nil {
				return
			}
			frames <- env
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{
		WorkerID: "w1", URLs: []string{wsURL}, Token: "t",
		PolicyMode: true, Reload: seam, CachePath: cachePath,
	}, &stubJobs{})
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cl.setConn(conn)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return cl, frames
}

// waitApplied reads frames until an Applied for rev arrives (fails on timeout).
func waitApplied(t *testing.T, frames chan wsproto.Envelope, rev int64) wsproto.Applied {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case env := <-frames:
			if env.Type != wsproto.TypeApplied {
				continue
			}
			a, err := wsproto.As[wsproto.Applied](env)
			if err != nil {
				t.Fatalf("decode applied: %v", err)
			}
			if a.Rev == rev {
				return a
			}
		case <-deadline:
			t.Fatalf("timed out waiting for an applied frame at rev %d", rev)
		}
	}
}

func hasDegrade(a wsproto.Applied, gate string) bool {
	for _, d := range a.Degraded {
		if d.Gate == gate {
			return true
		}
	}
	return false
}

// TestPolicyCacheRoundTrip (verification 9, cache): a written cache is read back as the
// SAME policy; a missing file is "no cache" (not an error); a foreign worker_id / a half
// file is an error the caller can WARN on (never a panic).
func TestPolicyCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-w1.policy.json")

	// Absent file → (nil, nil).
	if p, err := ReadPolicyCacheFile(path, "w1"); err != nil || p != nil {
		t.Fatalf("absent cache = (%+v,%v), want (nil,nil)", p, err)
	}

	src := pol(7, "alpha", "beta")
	if err := WritePolicyCacheFile(path, "w1", &src, 3); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	got, err := ReadPolicyCacheFile(path, "w1")
	if err != nil || got == nil {
		t.Fatalf("read cache = (%+v,%v), want the written policy", got, err)
	}
	if got.Rev != 7 || len(got.Projects) != 2 {
		t.Fatalf("round-tripped policy = %+v, want Rev 7 with 2 projects", got)
	}

	// Foreign worker_id → error, treated as no cache.
	if _, err := ReadPolicyCacheFile(path, "other"); err == nil {
		t.Fatal("a cache written by a different worker_id must be rejected")
	}

	// Half file → decode error, not a panic.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPolicyCacheFile(path, "w1"); err == nil {
		t.Fatal("a half/corrupt cache must be an error, not silently accepted")
	}

	// The written file is private (0600).
	if err := WritePolicyCacheFile(path, "w1", &src, 4); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %v, want 0600", fi.Mode().Perm())
	}
}

// TestPolicyCacheWriteFailureInvalidatesAndConverges (verification 9, cache-write-failure,
// D-HIGH-6): an apply whose cache write fails must (a) report policy_cache_stale on its
// Applied frame and (b) INVALIDATE the prior slot so a restart cannot replay the older Rev.
// It then converges to the newer Rev on a reconnect once the writer recovers.
//
// Falsification: drop the invalidate + degrade (treat a failed write as success). The
// "cache no longer holds the stale Rev 5" assertion then fails — a restart would replay
// Rev 5's now-revoked permissions.
func TestPolicyCacheWriteFailureInvalidatesAndConverges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-w1.policy.json")
	seam := &recordSeam{}
	cl, frames := newPolicyClientCache(t, seam.fn, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cl.reloadLoop(ctx)

	// Apply Rev 5 successfully: the cache holds Rev 5.
	gen := cl.beginSession(nil)
	cl.offerPolicy(gen, pol(5, "a"))
	a5 := waitApplied(t, frames, 5)
	if hasDegrade(a5, gateCacheStale) {
		t.Fatalf("Rev 5 must not be stale: %+v", a5.Degraded)
	}
	if p, err := ReadPolicyCacheFile(path, "w1"); err != nil || p == nil || p.Rev != 5 {
		t.Fatalf("cache after Rev 5 = (%+v,%v), want Rev 5", p, err)
	}

	// Force the cache write to fail, then apply Rev 6.
	cl.cacheMu.Lock()
	cl.writeCacheFn = func(*wsproto.Policy, uint64) error { return errors.New("injected rename failure") }
	cl.cacheMu.Unlock()
	cl.offerPolicy(gen, pol(6, "a"))
	a6 := waitApplied(t, frames, 6)
	if !hasDegrade(a6, gateCacheStale) {
		t.Fatalf("Rev 6 with a failed cache write must report %q, got %+v", gateCacheStale, a6.Degraded)
	}
	// The stale Rev 5 must be GONE (invalidated), never left to be replayed on restart.
	if p, err := ReadPolicyCacheFile(path, "w1"); err != nil {
		t.Fatalf("read after invalidation: %v", err)
	} else if p != nil {
		t.Fatalf("stale cache Rev %d survived a failed write; it must be invalidated", p.Rev)
	}

	// Recover the writer and reconnect (a v4 server re-pushes Rev 6). A fresh session
	// resets lastRev to 0, so Rev 6 is re-applied and the cache converges to Rev 6.
	cl.cacheMu.Lock()
	cl.writeCacheFn = func(p *wsproto.Policy, seq uint64) error { return WritePolicyCacheFile(path, "w1", p, seq) }
	cl.cacheMu.Unlock()
	gen2 := cl.beginSession(nil)
	cl.offerPolicy(gen2, pol(6, "a"))
	waitApplied(t, frames, 6)

	deadline := time.After(3 * time.Second)
	for {
		p, err := ReadPolicyCacheFile(path, "w1")
		if err == nil && p != nil && p.Rev == 6 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cache did not converge to Rev 6 (got %+v, err %v)", p, err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestPolicyCacheGuardDropsSupersededWrite (H4): a guarded write whose seq is no longer
// the latest apply token is dropped (never overwrites a newer Rev) — this is what stops a
// late retry of Rev 5 from clobbering an already-written Rev 6.
func TestPolicyCacheGuardDropsSupersededWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-w1.policy.json")
	cl, _ := newPolicyClientCache(t, (&recordSeam{}).fn, path)

	// Publish apply token 2 as the latest; a write tagged with an older seq 1 must be
	// dropped, and a write tagged with seq 2 must land.
	cl.applySeq.Store(2)
	rev6 := pol(6, "a")
	if err := cl.writePolicyCacheGuarded(&rev6, 2); err != nil {
		t.Fatalf("current-seq write must succeed: %v", err)
	}
	rev5 := pol(5, "a")
	if err := cl.writePolicyCacheGuarded(&rev5, 1); err != nil {
		t.Fatalf("superseded write must be a silent no-op (nil), got %v", err)
	}
	p, err := ReadPolicyCacheFile(path, "w1")
	if err != nil || p == nil || p.Rev != 6 {
		t.Fatalf("cache = (%+v,%v), want the newer Rev 6 (superseded Rev 5 write dropped)", p, err)
	}
}
