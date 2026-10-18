package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `RenewAuthGrant` 的客户端测试：请求打到续期路由、body 是 expires_sec 秒数，2xx 解出服务端报回的
// 那一条授权；非 2xx 的错误里带着服务端的理由（命令层直接显示它）。

func TestRenewAuthGrantPostsTheDeadline(t *testing.T) {
	var method, path, body, sessionHeader string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		sessionHeader = r.Header.Get(SessionHeader)
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte(`{"error":"the grant is not in force","detail":"grant g-1 expired at 1700000000: grant it again instead"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"g-1","subject":"session:s1","source":"db","state":"active",` +
			`"active":true,"effective":true,"expires_at":1800000000,"granted_by":"root",` +
			`"permissions":["session.reply.others"]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	got, err := c.RenewAuthGrant("g-1", 14400)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if method != http.MethodPost || path != "/v1/auth/grants/g-1/renew" {
		t.Fatalf("renewed over %s %s, want POST /v1/auth/grants/g-1/renew", method, path)
	}
	if body != `{"expires_sec":14400}` {
		t.Fatalf("body = %s, want the seconds of the requested extension", body)
	}
	if sessionHeader != "" {
		t.Fatalf("the renewal carried session %q; grant management does not ride a session", sessionHeader)
	}
	if got.ID != "g-1" || got.Subject != "session:s1" || got.State != "active" || !got.Effective ||
		got.ExpiresAt != 1800000000 || got.Permissions[0] != "session.reply.others" {
		t.Fatalf("the renewed grant = %+v", got)
	}

	// A grant that went out of force is refused by the server; the error has to carry the reason
	// (the CLI prints it verbatim) and the status.
	status = 409
	_, err = c.RenewAuthGrant("g-1", 14400)
	if err == nil || StatusOf(err) != 409 || !strings.Contains(err.Error(), "expired at 1700000000") {
		t.Fatalf("refused renewal = %v", err)
	}
}
