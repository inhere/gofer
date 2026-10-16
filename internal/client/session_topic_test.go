package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveCurrentSessionID(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if id, err := ResolveCurrentSessionID(env(map[string]string{"CLAUDE_CODE_SESSION_ID": "s1"})); err != nil || id != "s1" {
		t.Fatalf("one candidate = %q, %v", id, err)
	}
	if _, err := ResolveCurrentSessionID(env(nil)); err != ErrNoCurrentSession {
		t.Fatalf("none = %v", err)
	}
	_, err := ResolveCurrentSessionID(env(map[string]string{"CLAUDE_CODE_SESSION_ID": "s1", "CODEX_THREAD_ID": "s2"}))
	if err != ErrAmbiguousCurrentSession || !strings.Contains(err.Error(), "--session") {
		t.Fatalf("several = %v", err)
	}
}

func TestPatchSessionAndFind(t *testing.T) {
	var method, path, body string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte(`{"error":"patch session failed","detail":"session ended"}`))
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"sessions":[{"session_id":"s1","hits":[{"kind":"scope","match":"web"}],"maybe_offline":true}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"session_id":"s1","topic":"t","scope":["a"],"topic_source":"human"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	topic := "t"
	a, err := c.PatchSession("s1", SessionTopicPatch{Topic: &topic})
	if err != nil || a.Topic != "t" || a.TopicSource != "human" || a.Scope[0] != "a" || method != "PATCH" || path != "/v1/sessions/s1" || body != `{"topic":"t"}` {
		t.Fatalf("patch: %+v %v %s %s %s", a, err, method, path, body)
	}
	f, err := c.FindSessions(SessionFindOpts{Q: "x", Path: "web/src", Limit: 3})
	if err != nil || len(f) != 1 || !f[0].MaybeOffline || f[0].Hits[0].Kind != "scope" || !strings.Contains(path, "path=web%2Fsrc") {
		t.Fatalf("find: %+v %v %s", f, err, path)
	}
	if _, err := c.FindSessions(SessionFindOpts{}); err == nil {
		t.Fatal("empty find must fail")
	}
	status = 409
	_, err = c.PatchSession("s1", SessionTopicPatch{Topic: &topic})
	if err == nil || StatusOf(err) != 409 || !strings.Contains(err.Error(), "has ended") {
		t.Fatalf("ended = %v", err)
	}
}
