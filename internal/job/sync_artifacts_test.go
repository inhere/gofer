package job

import (
	"testing"
)

// TestSubmitSyncReachesTerminal: a fast job with sync=true blocks and returns the
// terminal result (async=false). Mirrors the HTTP sync-submit 200 path.
func TestSubmitSyncReachesTerminal(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	out, async, err := s.SubmitSync(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	}, true)
	if err != nil {
		t.Fatalf("SubmitSync: %v", err)
	}
	if async {
		t.Fatalf("expected sync (async=false) for a fast job")
	}
	if !IsTerminal(out.Status) {
		t.Fatalf("expected terminal status, got %q", out.Status)
	}
}

// TestSubmitSyncAsyncNotRequested: sync=false returns the submit snapshot without
// waiting (async=false, out is the initial result). Mirrors the HTTP default 200.
func TestSubmitSyncAsyncNotRequested(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	out, async, err := s.SubmitSync(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "sleep 1"}, Cwd: ".", TimeoutSec: 30,
	}, false)
	if err != nil {
		t.Fatalf("SubmitSync: %v", err)
	}
	if async {
		t.Fatalf("sync=false must not report async")
	}
	if out.ID == "" {
		t.Fatalf("expected a submit snapshot with an id")
	}
	// Let the background job drain before the temp dir is torn down.
	s.Wait(out.ID)
}

// TestSubmitSyncFallsBackToAsync: a job slower than the (clamped) wait cap returns
// async=true with the running snapshot. WaitTimeoutSec=1 keeps the wait short.
func TestSubmitSyncFallsBackToAsync(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	out, async, err := s.SubmitSync(JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", "sleep 3"}, Cwd: ".", TimeoutSec: 30,
		WaitTimeoutSec: 1,
	}, true)
	if err != nil {
		t.Fatalf("SubmitSync: %v", err)
	}
	if !async {
		t.Fatalf("expected async fallback for a job slower than the wait cap")
	}
	if IsTerminal(out.Status) {
		t.Fatalf("async fallback snapshot should still be running, got %q", out.Status)
	}
	s.Wait(out.ID)
}

// TestGetArtifactManifest: a finished job that wrote an artifact yields a non-nil
// manifest with the file, local source (Remote=false). An unknown id is ok=false.
func TestGetArtifactManifest(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)

	script := `mkdir -p "$GOFER_RESULT_DIR/artifacts" && ` +
		`printf 'hi' > "$GOFER_RESULT_DIR/artifacts/out.txt"`
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"sh", "-c", script}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("expected done, got %s", final.Status)
	}

	m, ok := s.GetArtifactManifest(final.ID)
	if !ok {
		t.Fatalf("GetArtifactManifest(%s): not found", final.ID)
	}
	if m.Remote {
		t.Fatalf("local job must report Remote=false, source=%q", m.Source)
	}
	if len(m.Items) == 0 {
		t.Fatalf("expected at least one artifact item")
	}
	var found bool
	for _, it := range m.Items {
		if it.Name == "out.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("artifact out.txt missing from manifest: %+v", m.Items)
	}

	if _, ok := s.GetArtifactManifest("no-such-id"); ok {
		t.Fatalf("unknown id must return ok=false")
	}
}

// TestIsRemoteSource locks the remote-source predicate (worker:/peer: are remote;
// empty/other are local) — the data-plane behind the artifact-download 409.
func TestIsRemoteSource(t *testing.T) {
	cases := map[string]bool{
		"":           false,
		"worker:abc": true,
		"peer:nodeA": true,
		"local":      false,
		"workerx":    false,
		"peerx":      false,
	}
	for src, want := range cases {
		if got := IsRemoteSource(src); got != want {
			t.Fatalf("IsRemoteSource(%q) = %v, want %v", src, got, want)
		}
	}
}
