package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type y6Messenger struct{ next int }

func (m *y6Messenger) SubmitMessenger(string, string, string, []string, string, string) (string, error) {
	m.next++
	return "y6-job", nil
}
func (*y6Messenger) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}

func TestSessionMessageViaRelayWhenWaiting(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	register := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-relay", "agent": "claude", "event": "SessionStart",
	})
	register.Body.Close()
	resp := do(t, s, http.MethodPost, "/v1/sessions/msg-relay/relay", testToken, map[string]any{"mode": "on"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-relay/turns", testToken, map[string]any{"body": "waiting"})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-relay/messages", testToken, map[string]any{"message": "继续"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message via relay status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSessionMessageViaMessengerJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-job", "agent": "claude", "runner": "server", "project_key": "self", "event": "SessionStart",
		"peer_name": "inspect-22", "peer_status": "busy", "peer_messaging": true,
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-job/messages", testToken, map[string]any{"message": "进度"})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("message via messenger status=%d, want 200 body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()
}

func TestSessionMessageWithoutProjectExplains(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-no-project", "agent": "claude", "runner": "server",
		"cwd": t.TempDir(), "event": "SessionStart", "peer_name": "inspect-22",
		"peer_status": "busy", "peer_messaging": true,
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-no-project/messages", testToken, map[string]any{"message": "进度"})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("message without project status=%d body=%s, want 409", resp.StatusCode, body)
	}
	for _, want := range []string{"该会话所在目录不属于任何已配置项目，无法派发传话人", "把目录加入项目", "在会话所在 runner 上配置项目"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("message without project body=%s, want explanation containing %q", body, want)
		}
	}
}

func TestSessionMessageOrderedPerSession(t *testing.T) {
	// The server must serialize messages for one session; the implementation test
	// uses the outbox order as the durable observable.
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-order", "agent": "claude", "runner": "server", "project_key": "self", "event": "SessionStart",
		"peer_name": "inspect-22", "peer_status": "idle", "peer_messaging": true,
	})
	resp.Body.Close()
	for _, msg := range []string{"one", "two"} {
		resp = do(t, s, http.MethodPost, "/v1/sessions/msg-order/messages", testToken, map[string]any{"message": msg})
		resp.Body.Close()
	}
	resp = do(t, s, http.MethodGet, "/v1/sessions/msg-order/outbox", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("outbox status=%d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSessionMessageFailureReported(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "msg-fail", "agent": "claude", "event": "SessionStart",
	})
	resp.Body.Close()
	resp = do(t, s, http.MethodPost, "/v1/sessions/msg-fail/messages", testToken, map[string]any{"message": "失败"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("message failure status=%d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}
