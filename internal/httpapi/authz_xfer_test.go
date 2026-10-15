package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/xfer"
)

// 传输（xfer）领域的旁路关闭用例与规则（设计
// docs/design/2026-10-14-db-capabilities-and-session-grants-design.md §1 的 `xfer.write` /
// `xfer.manage.others` 两行、§3「有意不保持现状的三处」#1；计划 T1.5g）。

// setTransferManager wires the transfer manager the transfer routes read their rows through.
// The harness server of TestInHandlerReverseCases mounts none (the routes answer 503 without
// one), so its T1.5g cases call this before the requests run.
func setTransferManager(t *testing.T, s *Server) {
	t.Helper()
	cfg := s.projects.Config()
	if cfg == nil {
		t.Fatal("the harness server has no configuration to root the transfer store in")
	}
	mgr, err := xfer.NewManager(xfer.Options{Root: cfg.Storage.Root, Repo: s.jobs.Meta()})
	if err != nil {
		t.Fatalf("new xfer manager: %v", err)
	}
	s.SetXfer(mgr)
	// G051: a transfer's background delivery must finish before the job service and the store
	// go. The reverse cases only read rows and never dispatch, so this is a guard.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait.Timeout(t, 10*time.Second))
		defer cancel()
		if err := mgr.Wait(ctx); err != nil {
			t.Errorf("xfer deliveries still running at cleanup: %v", err)
		}
	})
}

// TestTransferOfOthersIsTheRootsAlone closes the design's transfer bypass
// (「有意不保持现状的三处」#1: 「`xfer.manage.others` 不种入：他人的传输只有根管理员能读取、
// 下载」). A caller that owns nothing and holds nothing may not read, download or see another
// caller's transfer, the 403 names `xfer.manage.others`, and a root administrator does all
// three — while the initiating caller keeps its own transfer without any permission.
//
// The deployment deliberately keeps every governance switch OPEN: `xfer.manage.others` is 仅根,
// so no configuration derives it and the (seeded) `users` grants never carry it either. That is
// the difference from the old behaviour, which let any user or job credential download a
// finished get's content.
func TestTransferOfOthersIsTheRootsAlone(t *testing.T) {
	t.Parallel()
	s, mgr, _ := newXferServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
			{ID: "root", Token: "tok-root", CanAdmin: true},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}, xfer.Limits{})

	// Alice pulls a file back from the executing host; the row records HER as the caller.
	collected := []byte("collected for alice")
	sum := sha256.Sum256(collected)
	rec, err := mgr.StageGet("alice", "w-a", "demo", "tmp/out.bin")
	if err != nil {
		t.Fatalf("stage get: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if _, err := w.Write(collected); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := mgr.CommitGet(rec.ID, int64(len(collected)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("commit get: %v", err)
	}
	if err := mgr.MarkDone(rec.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	// The owner reads and downloads it without holding any permission: 「操作自己的资源不需要
	// 这类权限」.
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+rec.ID, "tok-alice", nil); code != http.StatusOK {
		t.Fatalf("the owner reading its own transfer = %d, want 200", code)
	}
	resp := do(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the owner downloading its own transfer = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()

	// Bob owns nothing: every one of the three is refused, and the refusal names the permission
	// (design「403 响应写明缺哪个权限」).
	for _, path := range []string{"/v1/xfer/" + rec.ID, "/v1/xfer/" + rec.ID + "/content"} {
		resp := do(t, s, http.MethodGet, path, "tok-bob", nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("bob reading %s = %d, want 403 (body %s)", path, resp.StatusCode, readBody(t, resp))
		}
		if body := readBody(t, resp); !strings.Contains(body, "xfer.manage.others") {
			t.Fatalf("the refusal of %s must name xfer.manage.others: %s", path, body)
		}
	}
	resp = do(t, s, http.MethodDelete, "/v1/xfer/"+rec.ID, "tok-bob", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bob deleting alice's transfer = %d, want 403 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()
	// The list is narrowed rather than refused (it is the filter form of the permission).
	resp = do(t, s, http.MethodGet, "/v1/xfer", "tok-bob", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob listing transfers = %d, want 200 (body %s)", resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); strings.Contains(body, rec.ID) {
		t.Fatalf("bob's list leaked alice's transfer %s: %s", rec.ID, body)
	}

	// A root administrator (can_admin — the only subject that holds `xfer.manage.others` in this
	// deployment) reads and downloads it.
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+rec.ID, "tok-root", nil); code != http.StatusOK {
		t.Fatalf("the root administrator reading the transfer = %d, want 200", code)
	}
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the root administrator downloading the transfer = %d, want 200 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	_ = resp.Body.Close()

	// The second transfer proves the download rule is the permission and not the id: bob's own
	// get is bob's to download, and alice may not touch it.
	bobRec, err := mgr.StageGet("bob", "w-a", "demo", "tmp/bob.bin")
	if err != nil {
		t.Fatalf("stage bob's get: %v", err)
	}
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+bobRec.ID, "tok-alice", nil); code != http.StatusForbidden {
		t.Fatalf("alice reading bob's transfer = %d, want 403", code)
	}
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+bobRec.ID, "tok-bob", nil); code != http.StatusOK {
		t.Fatalf("bob reading his own transfer = %d, want 200", code)
	}
}
