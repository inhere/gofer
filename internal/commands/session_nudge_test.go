package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionNudgeCLI(t *testing.T) {
	const sid = "9f2c1e40-1111-2222-3333-444455556666"
	type call struct{ method, path, body string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(b)})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nudges":
			_ = json.NewEncoder(w).Encode(map[string]any{"nudges": []map[string]any{
				{"id": "ng-1", "session_id": sid, "kind": "stalled", "interval_sec": 1200, "text": "status?", "state": "paused",
					"fail_count": 3, "pause_reason": "3 consecutive delivery failures"}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "ng-1", "session_id": sid, "kind": "every", "interval_sec": 1800, "state": "active"})
		}
	}))
	defer srv.Close()
	clientNode(t, srv.URL)

	set := func(every, stalled, msg, until string) error {
		c := bindCmd(newSessionNudgeCmd())
		c.Arg("sid").Set(sid)
		sessionNudgeOpts.every, sessionNudgeOpts.stalled, sessionNudgeOpts.message, sessionNudgeOpts.until = every, stalled, msg, until
		t.Cleanup(func() {
			sessionNudgeOpts.every, sessionNudgeOpts.stalled, sessionNudgeOpts.message, sessionNudgeOpts.until = "", "", "", ""
		})
		return runSessionNudgeSet(c, nil)
	}

	for name, args := range map[string][4]string{
		"neither":    {"", "", "x", ""},
		"both":       {"30m", "20m", "x", ""},
		"no message": {"30m", "", "", ""},
		"too short":  {"30s", "", "x", ""},
		"bad dur":    {"soon", "", "x", ""},
		"bad until":  {"30m", "", "x", "whenever"},
	} {
		if err := set(args[0], args[1], args[2], args[3]); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("invalid input reached the server: %+v", calls)
	}

	out := captureOutput(t, func() {
		if err := set("", "20m", "还在吗", "2h"); err != nil {
			t.Fatalf("set: %v", err)
		}
	})
	if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].path != "/v1/sessions/"+sid+"/nudges" {
		t.Fatalf("calls = %+v", calls)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(calls[0].body), &body)
	if body["kind"] != "stalled" || body["interval_sec"] != float64(1200) || body["text"] != "还在吗" || body["until_at"].(float64) < 1e9 {
		t.Fatalf("body = %s", calls[0].body)
	}
	if !strings.Contains(out, "ng-1") {
		t.Fatalf("out = %q", out)
	}

	calls = nil
	out = captureOutput(t, func() {
		c := bindCmd(findSub(t, newSessionNudgeCmd(), "list"))
		if err := runSessionNudgeList(c, nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if calls[0].path != "/v1/nudges" || !strings.Contains(out, "paused") || !strings.Contains(out, "status?") {
		t.Fatalf("list calls=%+v out=%q", calls, out)
	}

	for act, want := range map[string]struct{ method, path, body string }{
		"pause":  {http.MethodPatch, "/v1/nudges/ng-1", `{"state":"paused"}`},
		"resume": {http.MethodPatch, "/v1/nudges/ng-1", `{"state":"active"}`},
		"rm":     {http.MethodDelete, "/v1/nudges/ng-1", ""},
	} {
		calls = nil
		name := map[string]string{"rm": "remove"}[act]
		if name == "" {
			name = act
		}
		c := bindCmd(findSub(t, newSessionNudgeCmd(), name))
		c.Arg("id").Set("ng-1")
		captureOutput(t, func() {
			if err := runSessionNudgeAct(c, act); err != nil {
				t.Fatalf("%s: %v", act, err)
			}
		})
		if len(calls) != 1 || calls[0].method != want.method || calls[0].path != want.path || calls[0].body != want.body {
			t.Fatalf("%s calls = %+v", act, calls)
		}
	}
}
