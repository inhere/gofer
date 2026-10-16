package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func TestSSEClosesWhenSessionReadGrantIsRevoked(t *testing.T) {
	s := gatesOpenServer(t)
	if _, ok, err := s.jobs.Meta().RevokeAuthGrant("seed:users:"+authz.PermJobReadOthers, "root"); err != nil || !ok {
		t.Fatalf("remove seeded read grant: ok=%v err=%v", ok, err)
	}
	s.invalidateDBGrants()
	registerSessionAs(t, s, "tok-alice", "sess-sse-revoke")
	grant := createSessionGrant(t, s, "sess-sse-revoke", authz.PermJobReadOthers, 3600)
	resp := do(t, s, http.MethodPost, "/v1/jobs", "tok-root", job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "stdout-lines", "line", "100", "100ms"), Cwd: ".", TimeoutSec: 30,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create stream job = %d", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/jobs/"+created.ID+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok-alice")
	req.Header.Set(sessionHeader, "sess-sse-revoke")
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("SSE status=%d", stream.StatusCode)
	}
	waitLiveConn(t, s, 1, "SSE registration")
	revokeGrant(t, s, grant.ID)
	waitLiveConn(t, s, 0, "SSE revocation")
	closed := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, stream.Body); closed <- err }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE body stayed open after grant revocation")
	}
}

func TestAttachClosesWhenSessionAttachGrantIsRevoked(t *testing.T) {
	s := gatesOpenServer(t)
	registerSessionAs(t, s, "tok-alice", "sess-attach-revoke")
	grant := createSessionGrant(t, s, "sess-attach-revoke", authz.PermJobAttach, 3600)
	addAttachTicketJob(t, s, "job-attach-revoke", "alice")
	s.SetPtyRelay(ptyrelay.NewNonceStore(), ptyrelay.NewRegistry())
	src := newAttachFakeSource()
	openAttachRelay(t, s.ptyRelays, "job-attach-revoke", src)

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/job-attach-revoke/attach-ticket", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	req.Header.Set(sessionHeader, "sess-attach-revoke")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach ticket status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Ticket string `json:"ticket"`
	}
	decode(t, rec.Result(), &body)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+ts.URL[len("http"):]+"/v1/jobs/job-attach-revoke/attach?ticket="+body.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	waitLiveConn(t, s, 1, "attach registration")
	revokeGrant(t, s, grant.ID)
	waitLiveConn(t, s, 0, "attach revocation")
}
