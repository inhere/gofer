package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/updatecheck"
)

// otherPlatform is a platform different from the test's own.
func otherPlatform() (string, string) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		return "windows", "amd64"
	}
	return "linux", "arm64"
}

func hexSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// releaseMirror serves latest.yaml (0.160.0) plus the binaries of this platform and
// otherPlatform, and returns a checker that has already checked it.
func releaseMirror(t *testing.T) (*updatecheck.Checker, map[string][]byte) {
	t.Helper()
	oos, oarch := otherPlatform()
	bins := map[string][]byte{
		updatecheck.BinaryName(runtime.GOOS, runtime.GOARCH): []byte("own binary"),
		updatecheck.BinaryName(oos, oarch):                   []byte("other binary"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.yaml" {
			var b strings.Builder
			b.WriteString("version: 0.160.0\nfiles:\n")
			for name, body := range bins {
				b.WriteString("  " + name + ": " + hexSum(body) + "\n")
			}
			_, _ = w.Write([]byte(b.String()))
			return
		}
		if body, ok := bins[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	u := config.UpdateConfig{LatestURL: srv.URL + "/latest.yaml", URLTemplate: srv.URL + "/{name}-{os}-{arch}{ext}"}
	c := updatecheck.New("0.153.1", func() config.UpdateConfig { return u })
	if st := c.Check(context.Background()); st.Error != "" || !st.Newer {
		t.Fatalf("check: %+v", st)
	}
	return c, bins
}

func stubBinaryVersion(t *testing.T, version string) {
	t.Helper()
	old := binaryVersion
	binaryVersion = func(context.Context, string) (string, error) { return version, nil }
	t.Cleanup(func() { binaryVersion = old })
}

func getApply(t *testing.T, s *Server) updateResponse {
	t.Helper()
	resp := rawDo(t, s, http.MethodGet, "/v1/update", testToken, nil)
	defer resp.Body.Close()
	var out updateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A plain `gofer serve` cannot replace itself: apply is refused, managed=false.
func TestUpdateApplyNeedsManagedServer(t *testing.T) {
	s, _, _ := newUpgradeServer(t, nil)
	c, _ := releaseMirror(t)
	s.SetUpdateChecker(c)
	s.SetUpdateApply(t.TempDir(), nil)
	resp := rawDo(t, s, http.MethodPost, "/v1/update/apply", testToken, []byte(`{}`))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := getApply(t, s); got.Managed || !got.Newer {
		t.Fatalf("view = %+v", got)
	}
}

// The old server downloads every needed binary and hands its own to the managed
// upgrade; the new server then upgrades the workers (same platform: its own
// executable; another platform: the downloaded binary).
func TestUpdateApplyRestartsThenUpgradesWorkers(t *testing.T) {
	oos, oarch := otherPlatform()
	s, fu, mgr := newUpgradeServer(t, map[string]WorkerStatus{
		"w1": connectedWorker(runtime.GOOS, runtime.GOARCH, 15),
		"w2": connectedWorker(oos, oarch, 15),
	})
	fu.version = "0.160.0"
	c, bins := releaseMirror(t)
	s.SetUpdateChecker(c)
	stubBinaryVersion(t, "0.160.0 (abc1234)")
	var mu sync.Mutex
	var handed string
	s.SetUpdateApply(t.TempDir(), func(_ context.Context, binary string) error {
		mu.Lock()
		handed = binary
		mu.Unlock()
		return nil
	})

	resp := rawDo(t, s, http.MethodPost, "/v1/update/apply", testToken, []byte(`{"workers":["w1","w2"]}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	wait.Until(t, 10*time.Second, "the managed upgrade was not started", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return handed != ""
	})
	if !strings.HasSuffix(handed, updatecheck.BinaryName(runtime.GOOS, runtime.GOARCH)) {
		t.Fatalf("handed %q", handed)
	}
	if v := getApply(t, s); v.Apply == nil || v.Apply.Phase != applyRestarting || !v.Managed {
		t.Fatalf("before the restart: %+v", v.Apply)
	}
	if resp := rawDo(t, s, http.MethodPost, "/v1/update/apply", testToken, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second apply while one runs: %d", resp.StatusCode)
	}

	// The restart: the new server (now 0.160.0) resumes the apply.
	s.SetBuildInfo(buildinfo.Info{Version: "0.160.0", GitCommit: "abc1234"})
	s.ResumeUpdateApply(context.Background())
	v := getApply(t, s)
	if v.Apply.Phase != applyDone || len(v.Apply.Workers) != 2 {
		t.Fatalf("after the restart: %+v", v.Apply)
	}
	for _, w := range v.Apply.Workers {
		if w.State != "accepted" || w.UpgradeID == "" {
			t.Fatalf("worker %+v", w)
		}
	}
	if fu.called != 2 {
		t.Fatalf("worker upgrades sent: %d", fu.called)
	}
	if rec, _ := mgr.Latest("w2"); rec.TargetSHA256 != hexSum(bins[updatecheck.BinaryName(oos, oarch)]) {
		t.Fatalf("w2 got %s, want the %s/%s release binary", rec.TargetSHA256, oos, oarch)
	}
}

// A restart that did not take effect (same version) fails the apply instead of
// upgrading the workers; an offline worker is skipped up front.
func TestUpdateApplyRestartWithoutNewVersionFails(t *testing.T) {
	s, fu, _ := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker(runtime.GOOS, runtime.GOARCH, 15)})
	c, _ := releaseMirror(t)
	s.SetUpdateChecker(c)
	stubBinaryVersion(t, "0.160.0")
	done := make(chan struct{})
	s.SetUpdateApply(t.TempDir(), func(context.Context, string) error { close(done); return nil })
	if resp := rawDo(t, s, http.MethodPost, "/v1/update/apply", testToken, []byte(`{"workers":["w1","w2"]}`)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	select {
	case <-done:
	case <-time.After(wait.Timeout(t, 10*time.Second)):
		t.Fatal("the managed upgrade was not started")
	}
	v := getApply(t, s)
	if w := v.Apply.Workers[1]; w.ID != "w2" || w.State != "skipped" {
		t.Fatalf("offline worker: %+v", w)
	}
	s.ResumeUpdateApply(context.Background()) // still v9.9
	v = getApply(t, s)
	if v.Apply.Phase != applyFailed || !strings.Contains(v.Apply.Error, "after the restart") || fu.called != 0 {
		t.Fatalf("apply = %+v, upgrades %d", v.Apply, fu.called)
	}
}

// A downloaded binary reporting another version stops the apply before any restart.
func TestUpdateApplyWrongBinaryVersion(t *testing.T) {
	s, _, _ := newUpgradeServer(t, nil)
	c, _ := releaseMirror(t)
	s.SetUpdateChecker(c)
	stubBinaryVersion(t, "0.159.0")
	s.SetUpdateApply(t.TempDir(), func(context.Context, string) error {
		t.Error("must not restart")
		return nil
	})
	if resp := rawDo(t, s, http.MethodPost, "/v1/update/apply", testToken, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	wait.Until(t, 10*time.Second, "the apply did not fail", func() bool {
		v := getApply(t, s)
		return v.Apply != nil && v.Apply.Phase == applyFailed
	})
	if v := getApply(t, s); !strings.Contains(v.Apply.Error, "reports") {
		t.Fatalf("error = %q", v.Apply.Error)
	}
}
