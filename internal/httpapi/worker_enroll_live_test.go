package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/pushhub"
	"github.com/inhere/gofer/internal/today"
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

type todayCards struct {
	Snoozed   int `json:"snoozed"`
	Decisions []struct {
		Key       string `json:"key"`
		Kind      string `json:"kind"`
		Urgency   string `json:"urgency"`
		Title     string `json:"title"`
		Summary   string `json:"summary"`
		ExpiresAt int64  `json:"expires_at"`
		Actions   []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"actions"`
	} `json:"decisions"`
}

func (e *enrollEnv) todayRaw(token string) (todayCards, string) {
	e.t.Helper()
	rec := e.do(enrollReq{method: http.MethodGet, path: "/v1/today", token: token})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("today = %d: %s", rec.Code, rec.Body.String())
	}
	var out todayCards
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out, rec.Body.String()
}

func (c todayCards) enrollCards() (n int) {
	for _, d := range c.Decisions {
		if d.Kind == today.KindWorkerEnroll {
			n++
		}
	}
	return n
}

func TestTodayEnrollCardOnlyForWorkerManageWithPending(t *testing.T) {
	e := gatedEnroll(t, true)

	// A person without worker.manage never sees it.
	if got, _ := e.todayRaw("tok-op"); got.enrollCards() != 0 {
		t.Fatalf("op sees the enrollment card: %+v", got.Decisions)
	}
	// A holder sees exactly one, with the documented shape.
	got, raw := e.todayRaw("tok-root")
	if got.enrollCards() != 1 {
		t.Fatalf("root cards = %+v", got.Decisions)
	}
	var card = got.Decisions[len(got.Decisions)-1]
	if card.Kind != "worker_enroll" || card.Urgency != "now" || card.Title != "1 个 worker 申请注册" ||
		card.ExpiresAt == 0 || len(card.Actions) != 1 || card.Actions[0].Label != "去处理" || card.Actions[0].Value != "/runners#enrollments" {
		t.Fatalf("card = %+v", card)
	}
	if !strings.Contains(card.Summary, "box-1") || !strings.Contains(card.Summary, "gpu1") {
		t.Fatalf("summary = %q", card.Summary)
	}
	// No secret material: not the pairing code, the secret hash nor its source.
	rows, err := e.store.ListWorkerEnrollmentsAdmin(0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	for _, secret := range []string{rows[0].PairingCode, rows[0].SecretHash, sha256Hex(enrollSecret1), enrollSecret1} {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatalf("today response leaks %q", secret)
		}
	}
	// It never entered the snooze / advice paths.
	if got.Snoozed != 0 {
		t.Fatalf("snoozed = %d", got.Snoozed)
	}
	key := url.PathEscape(card.Key)
	if rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/today/snooze", token: "tok-root",
		body: map[string]any{"card_key": card.Key, "until_at": time.Now().Add(time.Hour).Unix()}}); rec.Code != http.StatusNotFound {
		t.Fatalf("snooze of the enrollment card = %d, want 404 (%s)", rec.Code, key)
	}
	if rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/today/advice", token: "tok-root",
		body: map[string]any{"card_key": card.Key, "text": "x"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("advice on the enrollment card = %d, want 404", rec.Code)
	}

	// Once nothing is pending, the card is gone.
	if rec := e.do(enrollReq{method: http.MethodPost, path: "/v1/worker-enrollments/" + rows[0].ID + "/reject",
		token: "tok-root", body: map[string]any{}}); rec.Code != http.StatusNoContent {
		t.Fatalf("reject = %d: %s", rec.Code, rec.Body.String())
	}
	if got, _ := e.todayRaw("tok-root"); got.enrollCards() != 0 {
		t.Fatalf("card with nothing pending: %+v", got.Decisions)
	}
}

func TestTodayEnrollCardAbsentWhenDisabled(t *testing.T) {
	e := gatedEnroll(t, true)
	e.mu.Lock()
	e.enabled = false
	e.mu.Unlock()
	if got, _ := e.todayRaw("tok-root"); got.enrollCards() != 0 {
		t.Fatalf("disabled feature still shows a card: %+v", got.Decisions)
	}
}
