package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/inhere/gofer/internal/wsproto"
)

func TestRunSessionLifecycleEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		var reg wsproto.Envelope
		if wsjson.Read(r.Context(), c, &reg) != nil {
			return
		}
		payload, _ := json.Marshal(wsproto.Registered{Accepted: true, ServerTime: time.Now().UnixMilli()})
		_ = wsjson.Write(r.Context(), c, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: payload})
	}))
	defer srv.Close()
	// A worker id of its own: slog is process-global, so a session leaked by
	// another test in this package keeps logging into our buffer, and its
	// worker.disconnected would otherwise land before our worker.registered and
	// fail the ordering assertion for the wrong reason.
	const workerID = "w-lifecycle"
	cl := New(Config{WorkerID: workerID, URLs: []string{"ws" + srv.URL[4:]}, ReadDeadline: 100 * time.Millisecond, PingInterval: time.Second}, nil)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := cl.runSession(ctx, cl.urls[0])
	if err == nil {
		t.Fatal("runSession should end when hub closes")
	}
	var events []string
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
		var row map[string]any
		if json.Unmarshal(line, &row) == nil {
			if e, ok := row["event"].(string); ok {
				if id, _ := row["worker_id"].(string); id != workerID {
					continue // another session's log line
				}
				events = append(events, e)
			}
		}
	}
	find := func(want string) int {
		for i, e := range events {
			if e == want {
				return i
			}
		}
		return -1
	}
	if find("worker.registered") < 0 || find("worker.disconnected") < 0 || find("worker.registered") > find("worker.disconnected") {
		t.Fatalf("lifecycle events=%v", events)
	}
}
