package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

type workDeleteResp struct {
	Deleted []string `json:"deleted"`
	Failed  []struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	} `json:"failed"`
}

func newWorkWith(t *testing.T, s *Server, title, status string) string {
	t.Helper()
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": title}, 200)
	if status != "" {
		w = workCall(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, testToken, map[string]any{"status": status}, 200)
	}
	return w.ID
}

func TestWorkItemDeletePermissions(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := newWorkWith(t, s, "to delete", "dropped")

	for name, kind := range map[string]string{"member": jobstore.JobCredentialMember, "leader": jobstore.JobCredentialLeader, "steward": jobstore.JobCredentialSteward} {
		tok := seedJobToken(t, s, "job-"+name, kind, "")
		resp := do(t, s, http.MethodDelete, "/v1/work-items/"+id, tok, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s DELETE = %d, want 403", name, resp.StatusCode)
		}
		resp.Body.Close()
		resp = do(t, s, http.MethodPost, "/v1/work-items/delete", tok, map[string]any{"ids": []string{id}})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s batch delete = %d, want 403", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
	workGet(t, s, "/v1/work-items/"+id) // still there

	// A person may delete it.
	resp := do(t, s, http.MethodDelete, "/v1/work-items/"+id, testToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("human DELETE = %d", resp.StatusCode)
	}
	resp.Body.Close()
	workCall(t, s, http.MethodGet, "/v1/work-items/"+id, testToken, nil, 404)
	workCall(t, s, http.MethodDelete, "/v1/work-items/"+id, testToken, nil, 404)
}

func TestWorkItemDeleteRefusesOpenItem(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := newWorkWith(t, s, "still open", "")
	workCall(t, s, http.MethodDelete, "/v1/work-items/"+id, testToken, nil, 400)
	workGet(t, s, "/v1/work-items/"+id)
}

func TestWorkItemBatchDelete(t *testing.T) {
	s := newTestServer(t, testToken, false)
	d1 := newWorkWith(t, s, "d1", "dropped")
	d2 := newWorkWith(t, s, "d2", "dropped")
	done := newWorkWith(t, s, "done", "done")
	open := newWorkWith(t, s, "open", "")

	// invalid status / empty body
	for _, body := range []map[string]any{{"status": "active"}, {}} {
		resp := do(t, s, http.MethodPost, "/v1/work-items/delete", testToken, body)
		if resp.StatusCode != 400 {
			t.Fatalf("body %v = %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// by status: only dropped go
	var out workDeleteResp
	resp := do(t, s, http.MethodPost, "/v1/work-items/delete", testToken, map[string]any{"status": "dropped"})
	if resp.StatusCode != 200 {
		t.Fatalf("batch = %d", resp.StatusCode)
	}
	decode(t, resp, &out)
	if len(out.Deleted) != 2 || len(out.Failed) != 0 {
		t.Fatalf("by status = %+v", out)
	}
	workCall(t, s, http.MethodGet, "/v1/work-items/"+d1, testToken, nil, 404)
	workCall(t, s, http.MethodGet, "/v1/work-items/"+d2, testToken, nil, 404)
	workGet(t, s, "/v1/work-items/"+done)

	// ids: the open one and an unknown id fail individually, the done one goes
	resp = do(t, s, http.MethodPost, "/v1/work-items/delete", testToken, map[string]any{"ids": []string{done, open, "w-nope"}})
	out = workDeleteResp{}
	decode(t, resp, &out)
	if len(out.Deleted) != 1 || out.Deleted[0] != done || len(out.Failed) != 2 {
		t.Fatalf("by ids = %+v", out)
	}
	workGet(t, s, "/v1/work-items/"+open)
}
