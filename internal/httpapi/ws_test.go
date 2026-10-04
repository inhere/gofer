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

	"github.com/inhere/gofer/internal/config"
)

func postWSTicket(t *testing.T, s *Server, token string) (int, string) {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/ws-ticket", token, nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return resp.StatusCode, ""
	}
	var body map[string]any
	decode(t, resp, &body)
	return resp.StatusCode, body["ticket"].(string)
}

func TestWSTicketRejectsJobAndWorkerCallers(t *testing.T) {
	t.Parallel()
	s := newCredentialServer(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
		Workers: map[string]config.WorkerAuthConfig{"w1": {Token: "tok-worker"}},
	}, nil, nil)

	code, ticket := postWSTicket(t, s, "tok-alice")
	if code != http.StatusOK || len(ticket) < 32 {
		t.Fatalf("user ticket: code=%d ticket=%q", code, ticket)
	}
	if code, _ := postWSTicket(t, s, "tok-worker"); code != http.StatusForbidden {
		t.Fatalf("worker token: code=%d, want 403", code)
	}
	jobTok := seedJobToken(t, s, "job-1", "member", "")
	if code, _ := postWSTicket(t, s, jobTok); code != http.StatusForbidden {
		t.Fatalf("job credential: code=%d, want 403", code)
	}
	if code, _ := postWSTicket(t, s, ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: code=%d, want 401", code)
	}
	// An attach ticket store is a different instance: it cannot be replayed here.
	if _, ok := s.attachTickets.Consume(ticket, time.Now().Unix()); ok {
		t.Fatal("ws ticket leaked into the attach store")
	}
}

func dialWS(t *testing.T, srv *httptest.Server, ticket, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opts := &websocket.DialOptions{}
	if origin != "" {
		opts.HTTPHeader = http.Header{"Origin": []string{origin}}
	}
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/ws?ticket="+ticket, opts)
}

func readWSFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("bad frame %q", data)
	}
	return m
}

func TestWSConnectHelloSubscribeAndLimits(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	_, ticket := postWSTicket(t, s, "tok-alice")
	conn, _, err := dialWS(t, srv, ticket, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	hello := readWSFrame(t, conn)
	if hello["t"] != "hello" || hello["server_time"] == nil {
		t.Fatalf("hello=%v", hello)
	}

	// A ticket is single use.
	if _, resp, err := dialWS(t, srv, ticket, ""); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed ticket should 401, err=%v resp=%v", err, resp)
	}

	// stats / pending are answered with a snapshot at subscribe time.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"sub","topics":["stats","pending","bogus"]}`)); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		m := readWSFrame(t, conn)
		switch {
		case m["t"] == "snap":
			seen[m["topic"].(string)] = true
			if m["topic"] == "pending" {
				d := m["data"].(map[string]any)
				if _, ok := d["interactions"].([]any); !ok {
					t.Fatalf("pending snapshot shape: %v", d)
				}
			}
		case m["t"] == "error":
			seen["error"] = true
		}
	}
	if !seen["stats"] || !seen["pending"] || !seen["error"] {
		t.Fatalf("frames seen=%v", seen)
	}

	// ping -> pong
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"t":"ping"}`))
	if m := readWSFrame(t, conn); m["t"] != "pong" {
		t.Fatalf("want pong, got %v", m)
	}

	// Per-caller cap: 16 connections, the 17th is refused.
	conns := []*websocket.Conn{conn}
	t.Cleanup(func() {
		for _, c := range conns {
			c.CloseNow()
		}
	})
	for i := 1; i < 16; i++ {
		_, tk := postWSTicket(t, s, "tok-alice")
		c, _, err := dialWS(t, srv, tk, "")
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	_, tk := postWSTicket(t, s, "tok-alice")
	if _, resp, err := dialWS(t, srv, tk, ""); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("17th connection should 429, err=%v resp=%v", err, resp)
	}
}

func TestWSOriginMustMatchTicket(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice"}},
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	// Ticket minted from one origin, redeemed from another.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/ws-ticket", nil)
	req.Header.Set("Authorization", "Bearer tok-alice")
	req.Header.Set("Origin", "http://good.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	ticket := body["ticket"].(string)
	if _, r, err := dialWS(t, srv, ticket, "http://evil.example"); err == nil || r == nil || r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("mismatched origin should 401, err=%v resp=%v", err, r)
	}
}
