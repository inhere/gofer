package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func writeACPFixture(t *testing.T, resultDir, stdout string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(resultDir, "artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"t":"prompt","text":"question"}`,
		`{"t":"thought","text":"old "}`,
		`{"t":"thought","text":"tokens"}`,
		`{"t":"tool_call","tool_call_id":"tc-1","title":"Edit main.go","kind":"edit","status":"pending","raw_input":"{\"path\":\"main.go\"}","locations":[{"path":"main.go","line":7}]}`,
		`{"t":"tool_call","tool_call_id":"tc-1","status":"completed"}`,
		`{"t":"permission","tool_call_id":"tc-1","outcome":"selected","option_id":"allow-once"}`,
		`{"t":"available_commands_update","raw":"noise"}`,
		`{"t":"session_info_update","raw":"noise"}`,
		`{"t":"plan","entries":[{"content":"finish","status":"completed"}]}`,
		`{"t":"usage_update","used":12}`,
		`{"t":"stop","stop_reason":"end_turn"}`,
	}
	if err := os.WriteFile(filepath.Join(resultDir, "artifacts", "acp.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDir, "stdout.log"), []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openACPStream(t *testing.T, base, jobID, query, token string) []sseEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/jobs/"+jobID+"/acp/stream"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open ACP stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ACP stream status=%d, want 200", resp.StatusCode)
	}
	return readFrames(t, resp, newFrameScanner(resp.Body), 10*time.Second)
}

func newFrameScanner(body io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(scanFrames)
	return scanner
}

func TestACPStreamEmitsStructuredEvents(t *testing.T) {
	t.Run("normalizes_and_tails_terminal_artifacts", func(t *testing.T) {
		s := newTestServer(t, testToken, false)
		srv := httptest.NewServer(s.Handler())
		defer srv.Close()

		id := createStreamJob(t, srv.URL, testcmd.Cmd(t, "printf", "original"))
		final := waitDoneHTTP(t, srv.URL, id)
		writeACPFixture(t, final.ResultDir, "fallback assistant\n")

		frames := openACPStream(t, srv.URL, id, "", testToken)
		var kinds []string
		var events []map[string]any
		var sawEnd bool
		for _, frame := range frames {
			if frame.Event == "end" {
				sawEnd = true
				continue
			}
			if frame.Event != "acp" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
				t.Fatalf("decode ACP frame %q: %v", frame.Data, err)
			}
			kind, _ := event["kind"].(string)
			kinds = append(kinds, kind)
			events = append(events, event)
		}
		wantKinds := []string{"prompt", "thought", "tool", "tool", "permission", "plan", "usage", "message", "stop"}
		if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") {
			t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
		}
		if events[1]["text"] != "old tokens" {
			t.Fatalf("merged thought = %#v", events[1])
		}
		if events[2]["tool_call_id"] != "tc-1" || events[3]["tool_call_id"] != "tc-1" || events[3]["status"] != "completed" {
			t.Fatalf("tool events = %#v / %#v", events[2], events[3])
		}
		if events[len(events)-2]["text"] != "fallback assistant\n" || !sawEnd {
			t.Fatalf("fallback/end missing: events=%#v end=%v", events, sawEnd)
		}

		tail := openACPStream(t, srv.URL, id, "?tail=2", testToken)
		var tailKinds []string
		for _, frame := range tail {
			if frame.Event != "acp" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
				t.Fatal(err)
			}
			tailKinds = append(tailKinds, event["kind"].(string))
			if event["kind"] == "truncated" && event["skipped"].(float64) <= 0 {
				t.Fatalf("truncated event = %#v", event)
			}
		}
		if strings.Join(tailKinds, ",") != "truncated,message,stop" {
			t.Fatalf("tail kinds = %v", tailKinds)
		}
	})

	t.Run("job_credential_get_matches_log_stream", func(t *testing.T) {
		const userToken = "tok-w3a-user"
		s := newCredentialServer(t, config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userToken}}},
			map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
		final := submitExecJob(t, s, userToken)
		writeACPFixture(t, final.ResultDir, "fallback\n")
		jobToken := seedJobToken(t, s, final.ID, jobstore.JobCredentialMember, "")
		srv := httptest.NewServer(s.Handler())
		defer srv.Close()
		for _, suffix := range []string{"/stream?tail=1", "/acp/stream?tail=1"} {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+final.ID+suffix, nil)
			req.Header.Set("Authorization", "Bearer "+jobToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s as job credential = %d, want 200", suffix, resp.StatusCode)
			}
		}
	})
}
