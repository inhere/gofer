package job

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// stubXfer is the job-side file seam a unit test drives: it answers from the test's
// own fixtures instead of the staging area/wire, and records what the job asked it to
// do. It is the whole reason the job service can be tested without an HTTP surface.
type stubXfer struct {
	// payload is what FetchUpload writes at dst (ignored when fetchErr is set).
	payload  []byte
	fetchErr error
	limits   CollectLimits
	owns     bool
	fetches  []fetchCall
}

// fetchCall is one FetchUpload request as the stub recorded it.
type fetchCall struct {
	xferID     string
	projectKey string
	dst        string
}

func (x *stubXfer) FetchUpload(_ context.Context, xferID, projectKey, dst string) error {
	x.fetches = append(x.fetches, fetchCall{xferID: xferID, projectKey: projectKey, dst: dst})
	if x.fetchErr != nil {
		return x.fetchErr
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, x.payload, 0o644)
}

// PullCollected is never reached from these tests: a job's own machine publishes what it
// collected (OwnsArtifacts) and the remote pull is covered end to end by the worker
// round trip, which stubs nothing.
func (x *stubXfer) PullCollected(context.Context, CollectedPull) (int64, error) {
	return 0, nil
}

func (x *stubXfer) CollectLimits() CollectLimits { return x.limits }
func (x *stubXfer) OwnsArtifacts() bool          { return x.owns }

// TestUploadPlacesFileBeforeAgent: a job's staged upload is materialized in its cwd
// BEFORE the agent starts — the agent reads the file it was promised, at the
// project-relative destination the request named.
func TestUploadPlacesFileBeforeAgent(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	xf := &stubXfer{payload: []byte("firmware-v1"), owns: true}
	s.SetXferBridge(xf)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		// cat-file proves the bytes the AGENT read: nothing else writes in.txt.
		Cmd:     testcmd.Cmd(t, "cat-file", "in.txt"),
		Uploads: []UploadSpec{{XferID: "xf-1", Dest: "in.txt"}},
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if out := jobStdout(t, root, final.ID); !strings.Contains(out, "firmware-v1") {
		t.Fatalf("the agent did not see the uploaded file in its cwd:\n%s", out)
	}
	if len(xf.fetches) != 1 {
		t.Fatalf("FetchUpload calls = %+v, want exactly one", xf.fetches)
	}
	if got := xf.fetches[0]; got.xferID != "xf-1" || got.projectKey != "self" {
		t.Fatalf("FetchUpload(%+v), want the staged id + the job's project", got)
	}
	if want := filepath.Join(root, "in.txt"); xf.fetches[0].dst != want {
		t.Fatalf("upload destination = %s, want %s (cwd-relative dest resolved on the executing machine)", xf.fetches[0].dst, want)
	}
	if final.Xfer == nil || len(final.Xfer.Uploads) != 1 {
		t.Fatalf("xfer summary = %+v, want one upload entry", final.Xfer)
	}
	if up := final.Xfer.Uploads[0]; !up.OK || up.Dest != "in.txt" || up.Size != int64(len("firmware-v1")) {
		t.Fatalf("upload entry = %+v, want ok/in.txt/%d", up, len("firmware-v1"))
	}
}

// TestUploadFailureFailsJobWithoutRunningAgent: an upload that cannot be placed fails
// the job with the destination named and the reason attached, and the agent is never
// started (a job that ran anyway would work on a cwd the caller did not describe).
func TestUploadFailureFailsJobWithoutRunningAgent(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	s.SetXferBridge(&stubXfer{fetchErr: errors.New("staged payload is gone")})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd:     testcmd.Cmd(t, "write-files", "ran.txt", "yes"),
		Uploads: []UploadSpec{{XferID: "xf-2", Dest: "tmp/in/a.bin"}},
	})
	if final.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", final.Status)
	}
	if !strings.Contains(final.Error, "upload tmp/in/a.bin: staged payload is gone") {
		t.Fatalf("error = %q, want it to name the destination and the reason", final.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "ran.txt")); !os.IsNotExist(err) {
		t.Fatalf("the agent ran despite the failed upload (stat err=%v)", err)
	}
	if types := eventTypes(t, s, final.ID); !hasSubsequence(types, []string{EventJobRunning, EventJobUploadFailed, EventJobTerminal}) {
		t.Fatalf("events = %v, want running → upload_failed → terminal", types)
	}
	if final.Xfer == nil || len(final.Xfer.Uploads) != 1 || final.Xfer.Uploads[0].OK {
		t.Fatalf("xfer summary = %+v, want one failed upload entry", final.Xfer)
	}
	if got := final.Xfer.Uploads[0].Error; !strings.Contains(got, "staged payload is gone") {
		t.Fatalf("upload error = %q, want the transfer's reason", got)
	}
}

// TestCollectGlobIntoArtifacts: after the job, the collect globs are matched in its
// cwd, each match lands in the job's artifacts as collected/<project-root-relative
// path>, the manifest lists it (so GET /v1/jobs/{id}/artifacts/... serves it), and a
// file over the transfer's per-file cap is SKIPPED with a reason instead of silently
// dropped.
func TestCollectGlobIntoArtifacts(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	s.SetXferBridge(&stubXfer{owns: true, limits: CollectLimits{MaxFile: 8, MaxTotal: 1 << 20}})

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd: testcmd.Cmd(t, "write-files",
			"out/a.csv", "1,2\n", "out/b.csv", "3,4\n", "out/big.csv", "0123456789"),
		Collect: []string{"out/*.csv"},
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Xfer == nil {
		t.Fatal("a job with collect globs must record an xfer summary")
	}
	names := make([]string, 0, len(final.Xfer.Collected))
	for _, f := range final.Xfer.Collected {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "out/a.csv,out/b.csv" {
		t.Fatalf("collected = %v, want the two files inside the per-file cap", names)
	}
	for _, f := range final.Xfer.Collected {
		if f.Size != 4 {
			t.Fatalf("collected %s size = %d, want 4", f.Name, f.Size)
		}
	}
	if len(final.Xfer.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want exactly the oversize file", final.Xfer.Skipped)
	}
	if sk := final.Xfer.Skipped[0]; sk.Reason != "too large" || !strings.HasSuffix(sk.Name, "big.csv") {
		t.Fatalf("skipped entry = %+v, want big.csv / too large", sk)
	}

	// The bytes are in the result dir where the artifact surface already looks.
	got, err := os.ReadFile(filepath.Join(final.ResultDir, "artifacts", "collected", "out", "a.csv"))
	if err != nil {
		t.Fatalf("read collected artifact: %v", err)
	}
	if string(got) != "1,2\n" {
		t.Fatalf("collected artifact = %q, want %q", got, "1,2\n")
	}
	if _, err := os.Stat(filepath.Join(final.ResultDir, "artifacts", "collected", "out", "big.csv")); err == nil {
		t.Fatal("an over-cap file must not be copied into the artifacts")
	}
	if !strings.Contains(final.ArtifactsJSON, "collected/out/a.csv") {
		t.Fatalf("artifact manifest = %s, want it to list collected/out/a.csv", final.ArtifactsJSON)
	}
	if types := eventTypes(t, s, final.ID); !hasSubsequence(types, []string{EventJobRunning, EventJobFilesCollected, EventJobTerminal}) {
		t.Fatalf("events = %v, want running → files_collected → terminal", types)
	}
}

// TestCollectRunsAfterVerifyAndOnFailure: collection is a job-level cleanup, not a
// success path — it runs after the verify step and for a job that failed, so a failed
// run still delivers the evidence (logs, partial output) it produced.
func TestCollectRunsAfterVerifyAndOnFailure(t *testing.T) {
	root := t.TempDir()
	s := newTestService(t, root)
	s.SetXferBridge(&stubXfer{owns: true, limits: CollectLimits{MaxFile: 1 << 20, MaxTotal: 1 << 20}})
	want := filepath.Join(root, "out", "pre.csv")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(want, []byte("pre\n"), 0o644); err != nil {
		t.Fatalf("seed cwd file: %v", err)
	}

	// (a) the AGENT fails: the file it left behind is still collected.
	failed := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd: testcmd.Cmd(t, "exit", "3"), Collect: []string{"out/*.csv"},
	})
	if failed.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", failed.Status)
	}
	if failed.Xfer == nil || len(failed.Xfer.Collected) != 1 || failed.Xfer.Collected[0].Name != "out/pre.csv" {
		t.Fatalf("collected after a failed agent = %+v, want out/pre.csv", failed.Xfer)
	}

	// (b) the VERIFY step fails (the agent was fine): collect still ran, and it ran
	// AFTER verify — the file the verify-failing run produced is in the summary.
	verifyFailed := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd:     testcmd.Cmd(t, "write-files", "out/v.csv", "v\n"),
		Verify:  testcmd.Cmd(t, "exit", "4"),
		Collect: []string{"out/*.csv"},
	})
	if verifyFailed.Status != StatusFailed || verifyFailed.Verify == nil || verifyFailed.Verify.Status != VerifyFailed {
		t.Fatalf("status/verify = %s/%+v, want a verify failure", verifyFailed.Status, verifyFailed.Verify)
	}
	names := make([]string, 0, 2)
	for _, f := range verifyFailed.Xfer.Collected {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "out/pre.csv,out/v.csv" {
		t.Fatalf("collected after a verify failure = %v, want both files (collect runs after verify)", names)
	}
}
