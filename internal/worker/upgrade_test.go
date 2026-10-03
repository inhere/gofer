package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/wsproto"
)

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestWorkerUpgradeVerifiesChecksum(t *testing.T) {
	p := filepath.Join(t.TempDir(), "worker.new")
	data := []byte("candidate")
	if err := os.WriteFile(p, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUpgradeFile(p, int64(len(data)), sha(data)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if err := VerifyUpgradeFile(p, int64(len(data)), "deadbeef"); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if err := VerifyUpgradeFile(p, int64(len(data))+1, sha(data)); err == nil {
		t.Fatal("bad size accepted")
	}
}

// fakeChild is a replacement process whose life the test scripts.
type fakeChild struct {
	exited chan struct{}
	killed atomic.Bool
}

func newFakeChild() *fakeChild { return &fakeChild{exited: make(chan struct{})} }

func (c *fakeChild) Pid() int                { return 4242 }
func (c *fakeChild) Exited() <-chan struct{} { return c.exited }
func (c *fakeChild) Kill() error {
	if c.killed.CompareAndSwap(false, true) {
		close(c.exited)
	}
	return nil
}

// upgradeHarness is an upgradeRun wired to temp files and scripted seams.
type upgradeHarness struct {
	t        *testing.T
	exe      string
	run      *upgradeRun
	payload  []byte
	req      wsproto.Upgrade
	inflight atomic.Int64

	mu       sync.Mutex
	holds    []bool
	exits    int
	spawned  []SpawnSpec
	accepted []string
	// spawn behaviour: called with the spec, returns the child (and may write the marker).
	onSpawn func(spec SpawnSpec, child *fakeChild)
	// observed when spawn is called: the binary content at exe, and in-flight count.
	exeAtSpawn    string
	flightAtSpawn int64
}

func newHarness(t *testing.T) *upgradeHarness {
	t.Helper()
	dir := t.TempDir()
	h := &upgradeHarness{t: t, exe: filepath.Join(dir, "gofer.exe"), payload: []byte("new-binary")}
	if err := os.WriteFile(h.exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.req = wsproto.Upgrade{RequestID: "up1", SHA256: sha(h.payload), Size: int64(len(h.payload)), URLPath: "/v1/workers/w1/upgrade/file"}
	deps := UpgradeDeps{
		ExePath:   h.exe,
		Args:      []string{"worker", "-d", "--upgrade-from", "1", "--upgrade-id=old", "--upgrade-ready", "/x/old"},
		Environ:   []string{"A=1"},
		ReadyPath: func(id string) string { return filepath.Join(dir, "ready-"+id) },
		LogPath:   filepath.Join(dir, "out.log"),
		Fetch: func(_ context.Context, urlPath, dst string, size int64) error {
			if urlPath != h.req.URLPath {
				t.Errorf("fetch path = %q", urlPath)
			}
			return os.WriteFile(dst, h.payload, 0o755)
		},
		CheckVersion: func(context.Context, string) (string, error) { return "v2", nil },
		Poll:         5 * time.Millisecond,
	}
	deps.Spawn = func(spec SpawnSpec) (ChildProc, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		cur, _ := os.ReadFile(h.exe)
		h.exeAtSpawn = string(cur)
		h.flightAtSpawn = h.inflight.Load()
		h.spawned = append(h.spawned, spec)
		child := newFakeChild()
		if h.onSpawn != nil {
			h.onSpawn(spec, child)
		}
		return child, nil
	}
	h.run = &upgradeRun{
		deps:     deps,
		inflight: func() int { return int(h.inflight.Load()) },
		hold:     func(v bool) { h.mu.Lock(); h.holds = append(h.holds, v); h.mu.Unlock() },
		exit:     func() { h.mu.Lock(); h.exits++; h.mu.Unlock() },
		pid:      777,
	}
	return h
}

func (h *upgradeHarness) do(ctx context.Context) error {
	return h.run.run(ctx, h.req, func(v string) {
		h.mu.Lock()
		h.accepted = append(h.accepted, v)
		h.mu.Unlock()
	})
}

func (h *upgradeHarness) exeContent() string {
	b, _ := os.ReadFile(h.exe)
	return string(b)
}

// writeMarkerOnSpawn makes the replacement "register" immediately.
func (h *upgradeHarness) writeMarkerOnSpawn() {
	h.onSpawn = func(spec SpawnSpec, _ *fakeChild) {
		marker := spec.Args[len(spec.Args)-1]
		if err := WriteReadyMarker(marker, "up1"); err != nil {
			h.t.Error(err)
		}
	}
}

func TestWorkerUpgradeRejectsBadDownload(t *testing.T) {
	h := newHarness(t)
	h.req.SHA256 = "deadbeef"
	err := h.do(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if len(h.accepted) != 0 || len(h.spawned) != 0 || h.exeContent() != "old-binary" {
		t.Fatalf("a bad download must not be accepted or installed: accepted=%v spawned=%d exe=%q", h.accepted, len(h.spawned), h.exeContent())
	}
	if _, serr := os.Stat(upgradeTempPath(h.exe)); !os.IsNotExist(serr) {
		t.Fatal("the rejected candidate must be removed")
	}
	// A candidate that does not run, and one reporting the wrong version, are rejected too.
	h = newHarness(t)
	h.run.deps.CheckVersion = func(context.Context, string) (string, error) { return "", errors.New("exec format error") }
	if err := h.do(context.Background()); err == nil || len(h.accepted) != 0 || h.exeContent() != "old-binary" {
		t.Fatalf("non-running candidate: err=%v accepted=%v", err, h.accepted)
	}
	h = newHarness(t)
	h.req.Version = "v9 (abc)"
	if err := h.do(context.Background()); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("version mismatch err = %v", err)
	}
}

func TestWorkerUpgradeDrainsBeforeSwitch(t *testing.T) {
	h := newHarness(t)
	h.writeMarkerOnSpawn()
	h.inflight.Store(2)
	// The jobs finish a bit later; the switch must not happen before that.
	go func() {
		time.Sleep(60 * time.Millisecond)
		h.inflight.Store(1)
		time.Sleep(60 * time.Millisecond)
		h.inflight.Store(0)
	}()
	if err := h.do(context.Background()); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if h.flightAtSpawn != 0 {
		t.Fatalf("new process started with %d job(s) still in flight", h.flightAtSpawn)
	}
	if h.exeAtSpawn != "new-binary" || len(h.accepted) != 1 || h.accepted[0] != "v2" {
		t.Fatalf("exe at spawn=%q accepted=%v", h.exeAtSpawn, h.accepted)
	}
	if h.exits != 1 {
		t.Fatalf("exits = %d, want 1 after the handover", h.exits)
	}
}

func TestWorkerUpgradeDrainTimeoutAbandons(t *testing.T) {
	h := newHarness(t)
	h.inflight.Store(1)
	h.run.drainLimit = 50 * time.Millisecond
	err := h.do(context.Background())
	if err == nil || !strings.Contains(err.Error(), "drain timed out") {
		t.Fatalf("err = %v, want drain timeout", err)
	}
	var rb *RolledBackError
	if errors.As(err, &rb) {
		t.Fatal("an abandoned drain is a failure, not a rollback: nothing was installed")
	}
	if len(h.spawned) != 0 || h.exeContent() != "old-binary" || h.exits != 0 || len(h.holds) != 0 {
		t.Fatalf("nothing may change: spawned=%d exe=%q exits=%d holds=%v", len(h.spawned), h.exeContent(), h.exits, h.holds)
	}
	if len(h.accepted) != 1 {
		t.Fatal("the request was accepted before the drain started")
	}
}

func TestWorkerUpgradeForceSkipsDrain(t *testing.T) {
	h := newHarness(t)
	h.writeMarkerOnSpawn()
	h.inflight.Store(3)
	h.req.Force = true
	if err := h.do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.flightAtSpawn != 3 || h.exits != 1 {
		t.Fatalf("force must switch without waiting: flight=%d exits=%d", h.flightAtSpawn, h.exits)
	}
}

func TestWorkerUpgradeHandoverSpawnSpec(t *testing.T) {
	h := newHarness(t)
	h.writeMarkerOnSpawn()
	if err := h.do(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec := h.spawned[0]
	marker := filepath.Join(filepath.Dir(h.exe), "ready-up1")
	want := []string{"worker", "-d", "--upgrade-from", "777", "--upgrade-id", "up1", "--upgrade-ready", marker}
	if spec.Exe != h.exe || !reflect.DeepEqual(spec.Args, want) || !reflect.DeepEqual(spec.Env, []string{"A=1"}) || spec.LogPath != filepath.Join(filepath.Dir(h.exe), "out.log") {
		t.Fatalf("spec = %+v\nwant args %v", spec, want)
	}
	if !reflect.DeepEqual(h.holds, []bool{true}) {
		t.Fatalf("reconnect holds = %v, want [true] (kept held until the process exits)", h.holds)
	}
	if _, err := os.Stat(h.exe + ".old"); err != nil {
		t.Fatalf("the old binary must be kept as .old: %v", err)
	}
}

func TestWorkerUpgradeRollsBackWhenNewFailsToRegister(t *testing.T) {
	h := newHarness(t)
	var child *fakeChild
	h.onSpawn = func(_ SpawnSpec, c *fakeChild) { child = c } // never writes the marker
	h.run.readyLimit = 80 * time.Millisecond
	err := h.do(context.Background())
	var rb *RolledBackError
	if !errors.As(err, &rb) || !strings.Contains(err.Error(), "did not register") {
		t.Fatalf("err = %v, want RolledBackError(did not register)", err)
	}
	if !child.killed.Load() {
		t.Fatal("the new process must be killed")
	}
	if h.exeContent() != "old-binary" {
		t.Fatalf("exe after rollback = %q, want the old binary restored", h.exeContent())
	}
	if h.exits != 0 {
		t.Fatal("a rolled-back worker keeps running")
	}
	if !reflect.DeepEqual(h.holds, []bool{true, false}) {
		t.Fatalf("holds = %v, want [true false]: reconnects must be allowed again", h.holds)
	}
}

func TestWorkerUpgradeRollsBackWhenNewProcessCrashes(t *testing.T) {
	h := newHarness(t)
	h.onSpawn = func(_ SpawnSpec, c *fakeChild) { _ = c.Kill() } // dies at once
	h.run.readyLimit = 5 * time.Second                           // must NOT wait this long
	started := time.Now()
	err := h.do(context.Background())
	var rb *RolledBackError
	if !errors.As(err, &rb) || !strings.Contains(err.Error(), "exited before registering") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(started) > 2*time.Second || h.exeContent() != "old-binary" {
		t.Fatalf("crash must roll back promptly (took %s, exe=%q)", time.Since(started), h.exeContent())
	}
}

func TestWorkerUpgradeRollsBackWhenSpawnFails(t *testing.T) {
	h := newHarness(t)
	h.run.deps.Spawn = func(SpawnSpec) (ChildProc, error) { return nil, errors.New("access denied") }
	err := h.do(context.Background())
	var rb *RolledBackError
	if !errors.As(err, &rb) || h.exeContent() != "old-binary" {
		t.Fatalf("err=%v exe=%q", err, h.exeContent())
	}
}

func TestSwitchBinaryRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "worker.exe")
	candidate := filepath.Join(dir, "worker.new")
	if err := os.WriteFile(target, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	rollback, err := SwitchBinary(target, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new" {
		t.Fatalf("after switch target=%q", got)
	}
	if err := rollback(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatalf("rollback target=%q err=%v", got, err)
	}
}

func TestStripUpgradeArgs(t *testing.T) {
	in := []string{"-c", "cfg", "worker", "--upgrade-from", "12", "-d", "--upgrade-id=abc", "--upgrade-ready", "/p", "--worker-config", "w.yaml"}
	want := []string{"-c", "cfg", "worker", "-d", "--worker-config", "w.yaml"}
	if got := StripUpgradeArgs(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseVersionOutput(t *testing.T) {
	got, err := parseVersionOutput("Version: \x1b[0;36m0.1.0-dev\x1b[0m\n")
	if err != nil || got != "0.1.0-dev" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := parseVersionOutput("  \n"); err == nil {
		t.Fatal("empty output accepted")
	}
}

func TestHandoverMarksReadyOnce(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ready")
	var ready int
	cl := New(Config{WorkerID: "w1", URLs: []string{"ws://x"}, Handover: &Handover{UpgradeID: "up9", FromPID: 5, ReadyPath: marker, OnReady: func() { ready++ }}}, &stubJobs{})
	if cl.handoverID() != "up9" {
		t.Fatalf("handoverID = %q", cl.handoverID())
	}
	cl.markHandoverReady()
	cl.markHandoverReady()
	if ready != 1 || !readyMarkerMatches(marker, "up9") || readyMarkerMatches(marker, "other") {
		t.Fatalf("ready=%d marker ok=%v", ready, readyMarkerMatches(marker, "up9"))
	}
}

// TestHandleUpgradeReplies pins the wire contract: the first frame is accepted (or the
// failure), a later failure is a second frame with the right phase, and a successful
// handover sends nothing more.
func TestHandleUpgradeReplies(t *testing.T) {
	cl, frames, _ := dialLiveClient(t, &stubJobs{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next := func() wsproto.UpgradeResult {
		r, err := wsproto.As[wsproto.UpgradeResult](waitFrame(t, frames, wsproto.TypeUpgradeResult))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	cl.upgradeFn = func(_ context.Context, _ wsproto.Upgrade, accepted func(string)) error {
		accepted("v2")
		return &RolledBackError{Err: errors.New("the new worker process did not register")}
	}
	cl.handleUpgrade(ctx, wsproto.Upgrade{RequestID: "r1"})
	if r := next(); !r.OK || r.Phase != wsproto.UpgradePhaseAccepted || r.Version != "v2" || r.RequestID != "r1" {
		t.Fatalf("first = %+v", r)
	}
	if r := next(); r.OK || r.Phase != wsproto.UpgradePhaseRolledBack || !strings.Contains(r.Error, "did not register") {
		t.Fatalf("second = %+v", r)
	}

	cl.upgradeFn = func(context.Context, wsproto.Upgrade, func(string)) error { return errors.New("checksum mismatch") }
	cl.handleUpgrade(ctx, wsproto.Upgrade{RequestID: "r2"})
	if r := next(); r.OK || r.Phase != wsproto.UpgradePhaseFailed || r.Error != "checksum mismatch" || r.RequestID != "r2" {
		t.Fatalf("early failure = %+v", r)
	}

	// Successful handover: accepted only, then silence (the new process's register is the signal).
	cl.upgradeFn = func(_ context.Context, _ wsproto.Upgrade, accepted func(string)) error { accepted("v3"); return nil }
	cl.handleUpgrade(ctx, wsproto.Upgrade{RequestID: "r3"})
	if r := next(); !r.OK {
		t.Fatalf("accepted = %+v", r)
	}
	select {
	case env := <-frames:
		if env.Type == wsproto.TypeUpgradeResult {
			t.Fatalf("a successful handover must send no further result: %+v", env)
		}
	case <-time.After(100 * time.Millisecond):
	}
}
