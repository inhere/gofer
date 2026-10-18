package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMailClientRoutes(t *testing.T) {
	type call struct{ method, uri, body, session string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(b), r.Header.Get(SessionHeader)})
		if r.URL.Path == "/v1/mail" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"mail": []any{map[string]any{"id": 3, "state": "pending", "body": "x"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 3, "state": "delivered", "mail": map[string]any{"id": 4, "state": "held"}, "held": true})
	}))
	defer srv.Close()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cur-sess")
	c := New(srv.URL, "tok")

	sent, err := c.SendMail("topic:x", "hi", true)
	if err != nil || !sent.Held || sent.Mail.ID != 4 {
		t.Fatalf("SendMail: %+v %v", sent, err)
	}
	list, err := c.ListMail(MailListOpts{Box: "out", Thread: "m1", State: "held", Session: "s", Limit: 5, Offset: 2})
	if err != nil || len(list) != 1 || list[0].ID != 3 {
		t.Fatalf("ListMail: %+v %v", list, err)
	}
	if _, err := c.GetMail(3); err != nil {
		t.Fatal(err)
	}
	if m, err := c.ReadMail(3); err != nil || m.State != "delivered" {
		t.Fatalf("ReadMail: %+v %v", m, err)
	}
	if _, err := c.ReplyMail(3, "ok", false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelMail(3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReaddressMail(3, "session:s"); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"POST", "/v1/mail", `{"body":"hi","to":"topic:x","urgent":true}`, "cur-sess"},
		{"GET", "/v1/mail?box=out&limit=5&offset=2&session=s&state=held&thread=m1", "", "cur-sess"},
		{"GET", "/v1/mail/3", "", "cur-sess"},
		{"GET", "/v1/mail/3", "", "cur-sess"},
		{"POST", "/v1/mail/3/ack", "{}", "cur-sess"},
		{"POST", "/v1/mail/3/reply", `{"body":"ok"}`, "cur-sess"},
		{"POST", "/v1/mail/3/cancel", "{}", "cur-sess"},
		{"POST", "/v1/mail/3/readdress", `{"to":"session:s"}`, "cur-sess"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

func TestMailClientSurfacesServerStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited","detail":"slow down"}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "tok").SendMail("caller", "x", false)
	if StatusOf(err) != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want a 429 status error", err)
	}
}
