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

// captureWorkerEvents installs a JSON slog handler capturing everything from
// debug up (the applied-frame line lives at DEBUG) and returns the rows for one
// worker id once the test is done.
func captureWorkerEvents(t *testing.T, workerID string) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return func() []map[string]any {
		var rows []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte{'\n'}) {
			var row map[string]any
			if json.Unmarshal(line, &row) == nil {
				if id, _ := row["worker_id"].(string); id == workerID {
					rows = append(rows, row)
				}
			}
		}
		return rows
	}
}

// findRow returns the FIRST captured row carrying event.
func findRow(rows []map[string]any, event string) map[string]any {
	for _, row := range rows {
		if row["event"] == event {
			return row
		}
	}
	return nil
}

// hubSession is a test hub that accepts one worker connection, answers the
// register frame, runs then closes with closeCode/reason.
func hubSession(t *testing.T, closeCode websocket.StatusCode, reason string, between func(ctx context.Context, c *websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		if wsjson.Write(r.Context(), c, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: payload}) != nil {
			return
		}
		if between != nil {
			between(r.Context(), c)
		}
		_ = c.Close(closeCode, reason)
	}))
}

// TestWorkerConnectReportsAttemptsAndDowntime (real path: runSession twice): the
// second registration says how long the worker had been away (downtime_ms) and
// how many attempts it took — the field the reconnect loop's `attempt` counter
// could not give, because it resets before the connect line is written.
func TestWorkerConnectReportsAttemptsAndDowntime(t *testing.T) {
	const workerID = "w-lg3-downtime"
	rows := captureWorkerEvents(t, workerID)
	srv := hubSession(t, websocket.StatusNormalClosure, "bye", nil)
	defer srv.Close()
	cl := New(Config{WorkerID: workerID, URLs: []string{"ws" + srv.URL[4:]}, ReadDeadline: 100 * time.Millisecond}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cl.runSession(ctx, cl.urls[0]); err == nil {
		t.Fatal("runSession should end when the hub closes")
	}
	first := findRow(rows(), "worker.registered")
	if first == nil {
		t.Fatal("worker.registered missing")
	}
	if first["attempts"] != float64(1) || first["downtime_ms"] != float64(0) {
		t.Fatalf("first connect row = %v", first)
	}

	time.Sleep(20 * time.Millisecond) // a measurable gap before the next session
	if _, err := cl.runSession(ctx, cl.urls[0]); err == nil {
		t.Fatal("runSession should end when the hub closes")
	}
	var second map[string]any
	for _, row := range rows() {
		if row["event"] == "worker.registered" {
			second = row // last one wins
		}
	}
	if second == nil {
		t.Fatal("second worker.registered missing")
	}
	dt, _ := second["downtime_ms"].(float64)
	if dt <= 0 {
		t.Fatalf("second connect must report the gap, row = %v", second)
	}
	if second["attempts"] != float64(1) {
		t.Fatalf("second connect attempts = %v", second["attempts"])
	}
}

// TestWorkerDisconnectCarriesCloseCode: the disconnect line carries the websocket
// close code as a field, so "the hub replaced us" (1000 + reason) is greppable
// without parsing the reason text.
func TestWorkerDisconnectCarriesCloseCode(t *testing.T) {
	const workerID = "w-lg3-closecode"
	rows := captureWorkerEvents(t, workerID)
	srv := hubSession(t, websocket.StatusNormalClosure, "replaced by new registration", nil)
	defer srv.Close()
	cl := New(Config{WorkerID: workerID, URLs: []string{"ws" + srv.URL[4:]}, ReadDeadline: 100 * time.Millisecond}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = cl.runSession(ctx, cl.urls[0])

	row := findRow(rows(), "worker.disconnected")
	if row == nil {
		t.Fatal("worker.disconnected missing")
	}
	if row["close_code"] != float64(websocket.StatusNormalClosure) {
		t.Fatalf("close_code = %v (row %v)", row["close_code"], row)
	}
	if reason, _ := row["reason"].(string); reason == "" {
		t.Fatalf("reason missing: %v", row)
	}
}

// TestUndecodableDispatchIsLoggedWithJobID: a dispatch frame this worker cannot
// decode never reaches handleDispatch, so the reject line is its only trace — and
// it must name the job.
func TestUndecodableDispatchIsLoggedWithJobID(t *testing.T) {
	const workerID = "w-lg3-baddispatch"
	rows := captureWorkerEvents(t, workerID)
	srv := hubSession(t, websocket.StatusNormalClosure, "bye", func(ctx context.Context, c *websocket.Conn) {
		_ = wsjson.Write(ctx, c, wsproto.Envelope{Type: wsproto.TypeDispatch, JobID: "job-bad", Payload: json.RawMessage(`"not-an-object"`)})
		time.Sleep(50 * time.Millisecond)
	})
	defer srv.Close()
	cl := New(Config{WorkerID: workerID, URLs: []string{"ws" + srv.URL[4:]}, ReadDeadline: time.Second}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = cl.runSession(ctx, cl.urls[0])

	row := findRow(rows(), "worker.dispatch_rejected")
	if row == nil {
		t.Fatalf("worker.dispatch_rejected missing: %v", rows())
	}
	if row["job_id"] != "job-bad" {
		t.Fatalf("job_id = %v", row["job_id"])
	}
	if row["level"] != "WARN" {
		t.Fatalf("level = %v", row["level"])
	}
}

// TestAppliedFrameUndeliveredIsDebug: a receipt that cannot be sent because the
// connection was replaced mid-apply is expected on every reconnect, so it is a
// DEBUG line with an event name (it used to be an unnamed WARN: 4973 of them on
// one flapping worker).
func TestAppliedFrameUndeliveredIsDebug(t *testing.T) {
	const workerID = "w-lg3-applied"
	rows := captureWorkerEvents(t, workerID)
	cl := New(Config{WorkerID: workerID}, nil) // no connection: the write must fail
	cl.writeAppliedFrame(context.Background(), wsproto.Applied{Rev: 7})

	row := findRow(rows(), "worker.applied_frame_undelivered")
	if row == nil {
		t.Fatalf("worker.applied_frame_undelivered missing: %v", rows())
	}
	if row["level"] != "DEBUG" {
		t.Fatalf("level = %v (want DEBUG)", row["level"])
	}
	if row["rev"] != float64(7) {
		t.Fatalf("rev = %v", row["rev"])
	}
	if msg, _ := row["error"].(string); msg == "" {
		t.Fatalf("error missing: %v", row)
	}
}
