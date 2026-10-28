package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var mailToolNames = []string{
	"gofer_session_mail_pause", "gofer_session_mail_resume", "gofer_session_mail_ack",
	"gofer_session_mail_send", "gofer_session_mail_list", "gofer_session_mail_read",
	"gofer_session_mail_reply", "gofer_session_mail_cancel",
}

var mailToolArgs = map[string]map[string]any{
	"gofer_session_mail_send":   {"to": []string{"caller"}, "body": "x"},
	"gofer_session_mail_pause":  {"session": "sid-a"},
	"gofer_session_mail_resume": {"session": "sid-a"},
	"gofer_session_mail_ack":    {"id": 3},
	"gofer_session_mail_list":   {},
	"gofer_session_mail_read":   {"id": 3},
	"gofer_session_mail_reply":  {"id": 3, "body": "x"},
	"gofer_session_mail_cancel": {"id": 3},
}

// Standalone mode has no mailbox: every tool answers the needs-server error.
func TestSessionMailToolsStandaloneNeedServer(t *testing.T) {
	s := connectScoped(t, "")
	res, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range res.Tools {
		have[tl.Name] = true
	}
	for _, name := range mailToolNames {
		if !have[name] {
			t.Fatalf("tool %s missing: %v", name, have)
		}
		r := callTool(t, s, name, mailToolArgs[name])
		b, _ := json.Marshal(r.Content)
		if !r.IsError || !strings.Contains(string(b), "needs a running gofer server") {
			t.Fatalf("%s: isErr=%v %s", name, r.IsError, b)
		}
	}
}

// Against a server each tool is the matching HTTP call.
func TestSessionMailToolsCallTheServer(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		if r.URL.Path == "/v1/mail" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"mail":[{"id":3,"state":"pending","body":"x"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":3,"state":"delivered","mail":{"id":3,"state":"pending"}}`))
	}))
	defer srv.Close()
	for _, name := range mailToolNames {
		if isErr, text := resultText(t, mailToolArgs[name], name, srv.URL); isErr {
			t.Fatalf("%s: %s", name, text)
		}
	}
	want := []string{
		`POST /v1/sessions/sid-a/mail/pause {}`,
		`POST /v1/sessions/sid-a/mail/resume {}`,
		`POST /v1/mail/3/ack {"done":false}`,
		`POST /v1/mail {"body":"x","to":["caller"]}`,
		`GET /v1/mail `,
		`GET /v1/mail/3 `,
		`POST /v1/mail/3/ack {}`,
		`POST /v1/mail/3/reply {"body":"x"}`,
		`POST /v1/mail/3/cancel {}`,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q\nwant   %q", got, want)
	}
}

func TestSessionMailToolsValidateInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("an invalid call reached the server: %s %s", r.Method, r.URL)
	}))
	defer srv.Close()
	for name, args := range map[string]map[string]any{
		"gofer_session_mail_send":  {"to": []string{"caller"}, "body": " "},
		"gofer_session_mail_read":  {},
		"gofer_session_mail_reply": {"id": 3},
	} {
		if isErr, _ := resultText(t, args, name, srv.URL); !isErr {
			t.Errorf("%s with %v must be an error", name, args)
		}
	}
}
