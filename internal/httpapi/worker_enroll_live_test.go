package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/workerenroll"
)

// gatedEnroll returns an enrollment env whose server requires the admin capability (only
// "root" holds worker.manage; "op" does not) and a pending request made before the swap.
func gatedEnroll(t *testing.T, withPending bool) *enrollEnv {
	t.Helper()
	e := newEnrollEnv(t, true)
	if withPending {
		e.request("198.51.100.7:1")
	}
	e.s = newTestServerCfg(t, adminGroupConfig(true))
	svc, err := workerenroll.New(workerenroll.Options{Store: e.store, Registrar: e, Audit: e, Config: e})
	if err != nil {
		t.Fatal(err)
	}
	e.s.SetWorkerEnrollment(svc)
	return e
}

func TestWSEnrollmentTopicNeedsWorkerManage(t *testing.T) {
	e := gatedEnroll(t, false)
	srv := httptest.NewServer(e.s.Handler())
	t.Cleanup(srv.Close)

	subscribe := func(token string) (rejected []any, conn *websocket.Conn) {
		_, ticket := postWSTicket(t, e.s, token)
		conn, _, err := dialWS(t, srv, ticket, "")
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if hello := readWSFrame(t, conn); hello["t"] != "hello" {
			t.Fatalf("hello = %v", hello)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"sub","topics":["worker-enrollments","stats"]}`)); err != nil {
			t.Fatal(err)
		}
		// Frames of one connection are ordered: the pong closes the window in which a
		// refusal (error frame) for the sub above could still arrive.
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"ping"}`)); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			m := readWSFrame(t, conn)
			if m["t"] == "error" {
				rejected, _ = m["topics"].([]any)
			}
			if m["t"] == "pong" {
				break
			}
		}
		return rejected, conn
	}

	rejected, c1 := subscribe("tok-op")
	defer c1.CloseNow()
	if len(rejected) != 1 || rejected[0] != pushhub.TopicWorkerEnrollments {
		t.Fatalf("a caller without worker.manage must be refused the topic, rejected=%v", rejected)
	}
	if got := e.s.Live().Subscribers(pushhub.TopicWorkerEnrollments); got != 0 {
		t.Fatalf("refused subscriber count = %d", got)
	}

	rejected, c2 := subscribe("tok-root")
	defer c2.CloseNow()
	if len(rejected) != 0 {
		t.Fatalf("worker.manage holder was refused: %v", rejected)
	}
	waitSubscribers(t, e, 1)
	// The connection's re-declared needs now include the permission.
	found := false
	for _, lc := range e.s.liveConns.snapshot() {
		for _, p := range lc.perms {
			found = found || p == authz.PermWorkerManage
		}
	}
	if !found {
		t.Fatal("subscription must re-declare worker.manage on the live connection")
	}
}

func waitSubscribers(t *testing.T, e *enrollEnv, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.s.Live().Subscribers(pushhub.TopicWorkerEnrollments) == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscribers = %d, want %d", e.s.Live().Subscribers(pushhub.TopicWorkerEnrollments), n)
}

func TestWSEnrollmentTopicPermsAreRedeclared(t *testing.T) {
	e := gatedEnroll(t, false)
	if got := e.s.wsTopicPerms(nil, "root", "", pushhub.TopicWorkerEnrollments); len(got) != 1 || got[0] != authz.PermWorkerManage {
		t.Fatalf("perms = %v", got)
	}
}

func TestWSDefaultConnectionHasNoEnrollmentTopic(t *testing.T) {
	e := gatedEnroll(t, false)
	srv := httptest.NewServer(e.s.Handler())
	t.Cleanup(srv.Close)
	_, ticket := postWSTicket(t, e.s, "tok-root")
	conn, _, err := dialWS(t, srv, ticket, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if hello := readWSFrame(t, conn); hello["t"] != "hello" {
		t.Fatalf("hello = %v", hello)
	}
	if got := e.s.Live().Subscribers(pushhub.TopicWorkerEnrollments); got != 0 {
		t.Fatalf("a connection that sent no sub is subscribed to the topic (%d)", got)
	}
}

// The pending worker enrollments are not a card of /v1/today (CL1): the Runners page and the
// top-bar count read GET /v1/worker-enrollments themselves, and the today payload carries
// neither a `decisions` queue nor an enrollment card for anybody.
func TestTodayCarriesNoEnrollCard(t *testing.T) {
	e := gatedEnroll(t, true)
	rows, err := e.store.ListWorkerEnrollmentsAdmin(0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, token := range []string{"tok-op", "tok-root"} {
		rec := e.do(enrollReq{method: http.MethodGet, path: "/v1/today", token: token})
		if rec.Code != http.StatusOK {
			t.Fatalf("today = %d: %s", rec.Code, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["decisions"]; ok {
			t.Fatalf("%s: /v1/today still carries decisions: %s", token, rec.Body.String())
		}
		body := rec.Body.String()
		if strings.Contains(body, "worker_enroll") {
			t.Fatalf("%s: /v1/today carries an enrollment card: %s", token, body)
		}
		// No secret material either.
		for _, secret := range []string{rows[0].PairingCode, rows[0].SecretHash, sha256Hex(enrollSecret1), enrollSecret1} {
			if secret != "" && strings.Contains(body, secret) {
				t.Fatalf("today response leaks %q", secret)
			}
		}
	}
}
