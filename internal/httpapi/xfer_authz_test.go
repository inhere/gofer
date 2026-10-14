package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/xfer"
)

// TestXferRecordsAreTheOwnersBusiness (gofer-hxld): a transfer belongs to the caller
// that initiated it. Another caller may not read its journal line, download its content
// or delete it; a root administrator may. The list is narrowed to the caller's own
// transfers rather than refused, so `xfer ls` stays usable.
func TestXferRecordsAreTheOwnersBusiness(t *testing.T) {
	t.Parallel()
	sc := config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "alice", Token: "tok-alice"},
			{ID: "bob", Token: "tok-bob"},
			{ID: "root", Token: "tok-root", CanAdmin: true},
		},
		Workers: map[string]config.WorkerAuthConfig{"w-a": {Token: "tok-w-a"}},
	}
	s, mgr, _ := newXferServer(t, sc, xfer.Limits{})

	// Alice pushes a file: the row records HER as the initiating caller.
	secret := []byte("alice's private file")
	resp, body := xferPush(t, s, "tok-alice", pushMeta("tmp/secret.bin", secret, "", true), "secret.bin", secret)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice push status=%d body=%v, want 200", resp.StatusCode, body)
	}
	putID, _ := body["id"].(string)
	if putID == "" {
		t.Fatalf("push body=%v, want an id", body)
	}

	// And a get whose result the assigned worker uploaded, so there is real content to
	// download.
	collected := []byte("collected for alice")
	sum := sha256.Sum256(collected)
	getRec, err := mgr.StageGet("alice", "w-a", "demo", "tmp/out.bin")
	if err != nil {
		t.Fatalf("stage get: %v", err)
	}
	w, err := mgr.Store().Writer(getRec.ID)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if _, err := w.Write(collected); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := mgr.CommitGet(getRec.ID, int64(len(collected)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("commit get: %v", err)
	}
	if err := mgr.MarkDone(getRec.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	// The owner reads all three, and downloads the result.
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+getRec.ID, "tok-alice", nil); code != http.StatusOK {
		t.Fatalf("owner status read=%d, want 200", code)
	}
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+getRec.ID+"/content", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner download status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Another caller is refused on every one of them — and told whose transfer it is.
	for _, path := range []string{"/v1/xfer/" + getRec.ID, "/v1/xfer/" + putID, "/v1/xfer/" + getRec.ID + "/content"} {
		if code := statusOf(t, s, http.MethodGet, path, "tok-bob", nil); code != http.StatusForbidden {
			t.Fatalf("foreign read %s status=%d, want 403", path, code)
		}
	}
	if code := statusOf(t, s, http.MethodDelete, "/v1/xfer/"+putID, "tok-bob", nil); code != http.StatusForbidden {
		t.Fatalf("foreign delete status=%d, want 403", code)
	}

	// The list is narrowed, not refused: bob sees his own (none), the owner sees both,
	// the administrator sees both.
	listIDs := func(token string) []string {
		t.Helper()
		resp := do(t, s, http.MethodGet, "/v1/xfer", token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list status=%d, want 200", resp.StatusCode)
		}
		var out struct {
			Xfers []xferView `json:"xfers"`
		}
		decode(t, resp, &out)
		ids := make([]string, 0, len(out.Xfers))
		for _, v := range out.Xfers {
			ids = append(ids, v.ID)
		}
		return ids
	}
	if ids := listIDs("tok-bob"); len(ids) != 0 {
		t.Fatalf("bob's list = %v, want none of alice's transfers", ids)
	}
	for _, token := range []string{"tok-alice", "tok-root"} {
		ids := listIDs(token)
		if len(ids) != 2 {
			t.Fatalf("%s list = %v, want both transfers", token, ids)
		}
	}

	// A root administrator reads and downloads anything.
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+putID, "tok-root", nil); code != http.StatusOK {
		t.Fatalf("admin status read=%d, want 200", code)
	}
	resp = do(t, s, http.MethodGet, "/v1/xfer/"+getRec.ID+"/content", "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin download status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// The administrator may also delete another caller's transfer; the owner alone may
	// delete their own.
	if code := statusOf(t, s, http.MethodDelete, "/v1/xfer/"+putID, "tok-root", nil); code != http.StatusOK {
		t.Fatalf("admin delete status=%d, want 200", code)
	}
	if code := statusOf(t, s, http.MethodDelete, "/v1/xfer/"+getRec.ID, "tok-alice", nil); code != http.StatusOK {
		t.Fatalf("owner delete status=%d, want 200", code)
	}
}

// TestXferJobCredentialIsNotAnOwner: a job credential reads the GET surface by
// permission table, but a transfer id is not a capability for a job either — only the
// person that initiated it (or an administrator) downloads its content. The local job
// file seam never goes through HTTP (core.hubJobXfer reads the staging area directly),
// so nothing a job does depends on this.
func TestXferJobCredentialIsNotAnOwner(t *testing.T) {
	t.Parallel()
	s, mgr, _ := newXferServer(t, config.ServerConfig{Token: testToken}, xfer.Limits{})
	data := []byte("for the operator")
	sum := sha256.Sum256(data)
	rec, err := mgr.StageGet("default", "local", "demo", "tmp/out.csv")
	if err != nil {
		t.Fatalf("stage get: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := mgr.CommitGet(rec.ID, int64(len(data)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("commit get: %v", err)
	}
	if err := mgr.MarkDone(rec.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	tok := seedJobToken(t, s, "job-1", jobstore.JobCredentialMember, "")
	if code := statusOf(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", tok, nil); code != http.StatusForbidden {
		t.Fatalf("job content download status=%d, want 403", code)
	}
	if code := statusOf(t, s, http.MethodDelete, "/v1/xfer/"+rec.ID, tok, nil); code != http.StatusForbidden {
		t.Fatalf("job delete status=%d, want 403", code)
	}
	// The owner (the legacy token's caller, "default") still downloads it.
	resp := do(t, s, http.MethodGet, "/v1/xfer/"+rec.ID+"/content", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner download status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}
