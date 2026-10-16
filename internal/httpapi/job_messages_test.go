package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// messagesRulesFrame mirrors job.rulesPromptHeader + job.rulesPromptEnd (unexported
// literals, frozen by design): the frame the server injects at the top of a job's
// prompt, which the detail page's 消息 list must strip and offer separately.
const messagesRulesFrame = "## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）\n\n" +
	"### house-rules\n- never push\n\n<!-- gofer:rules-end -->"

// addMessageJob seeds one job row whose stored request carries the prompt the 消息
// list reads a user message from.
func addMessageJob(t *testing.T, s *Server, id, caller, sessionID, prompt string, startedAt int64) job.JobResult {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"prompt": prompt, "project_key": "self"})
	if err != nil {
		t.Fatal(err)
	}
	rec := jobstore.JobRecord{
		ID: id, ProjectKey: "self", Agent: "exec", Runner: "local", Status: "done",
		Cwd: ".", ResultDir: filepath.Join(t.TempDir(), id), StartedAt: startedAt,
		EndedAt: startedAt + 5, UpdatedAt: startedAt, CallerID: caller, SessionID: sessionID,
		RequestJSON: string(raw),
	}
	if err := os.MkdirAll(rec.ResultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.jobs.Meta().UpsertJob(rec); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	res, ok := s.jobs.Get(id)
	if !ok {
		t.Fatalf("job %s not readable after upsert", id)
	}
	return res
}

// writeMessageTranscript seeds one job's ACP structured record (artifacts/acp.jsonl).
func writeMessageTranscript(t *testing.T, res job.JobResult, records ...map[string]any) {
	t.Helper()
	dir := filepath.Join(res.ResultDir, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, rec := range records {
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "acp.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// getJobMessages fetches the detail's 消息 list for one job.
func getJobMessages(t *testing.T, s *Server, id, token string) []jobMessageView {
	t.Helper()
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+id+"?include=messages", token, nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("include=messages status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Messages []jobMessageView  `json:"messages"`
		Errors   map[string]string `json:"include_errors"`
	}
	decode(t, resp, &body)
	if len(body.Errors) > 0 {
		t.Fatalf("include_errors = %v, want none", body.Errors)
	}
	return body.Messages
}

func messagesCallers() config.ServerConfig {
	return config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}}
}

// TestJobDetailMessagesFirstPrompt covers a plain job: the stored prompt is the one
// message, with the injected rules frame split off into `injected` so the page can
// offer the full text behind a toggle (gofer-chd9).
func TestJobDetailMessagesFirstPrompt(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, messagesCallers())
	now := time.Now().Unix()
	addMessageJob(t, s, "job-plain", "alice", "", messagesRulesFrame+"\n\n修一下登录页。", now)

	msgs := getJobMessages(t, s, "job-plain", "tok-alice")
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want one", msgs)
	}
	got := msgs[0]
	if got.Role != roleUser || got.JobID != "job-plain" || got.At != now {
		t.Fatalf("message = %+v, want the job's own prompt at %d", got, now)
	}
	if got.Text != "修一下登录页。" {
		t.Fatalf("text = %q, want the caller's own words", got.Text)
	}
	if got.Injected != messagesRulesFrame {
		t.Fatalf("injected = %q, want the injected rules frame", got.Injected)
	}
	if got.Truncated || got.Omitted {
		t.Fatalf("unexpected flags: %+v", got)
	}
}

// TestJobDetailMessagesSessionChain: a resumed session shows every turn's user
// message oldest-first, and the rules frame is carried only on the first one (every
// turn gets the same frame; repeating it would bury the conversation).
func TestJobDetailMessagesSessionChain(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, messagesCallers())
	now := time.Now().Unix()
	addMessageJob(t, s, "job-t2", "alice", "sess-1", "第二轮：改一下文案。", now+60)
	addMessageJob(t, s, "job-t1", "alice", "sess-1", messagesRulesFrame+"\n\n第一轮：修登录页。", now)

	msgs := getJobMessages(t, s, "job-t2", "tok-alice")
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v, want the session's two turns", msgs)
	}
	if msgs[0].JobID != "job-t1" || msgs[0].Text != "第一轮：修登录页。" || msgs[0].Injected == "" {
		t.Fatalf("first message = %+v, want the session's first turn with its rules frame", msgs[0])
	}
	if msgs[1].JobID != "job-t2" || msgs[1].Text != "第二轮：改一下文案。" || msgs[1].Injected != "" {
		t.Fatalf("second message = %+v, want the later turn without a repeated frame", msgs[1])
	}
}

// TestJobDetailMessagesACPTranscript: the say turns of a resident ACP session and the
// agent's replies live only in the job's structured record, so the list reads them
// from there (in file order, after the job's own stored prompt).
func TestJobDetailMessagesACPTranscript(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, messagesCallers())
	now := time.Now().Unix()
	res := addMessageJob(t, s, "job-acp", "alice", "", messagesRulesFrame+"\n\n启动 acp job 的消息。", now)
	longReply := strings.Repeat("答", messagesReplyRunes+50)
	writeMessageTranscript(t, res,
		map[string]any{"t": "session", "session_id": "sess-acp"},
		map[string]any{"t": "prompt", "text": messagesRulesFrame + "\n\n启动 acp job 的消息。"},
		map[string]any{"t": "message", "text": "第一条回复。"},
		map[string]any{"t": "prompt", "text": "say 问一句"},
		map[string]any{"t": "message", "text": longReply},
	)

	msgs := getJobMessages(t, s, "job-acp", "tok-alice")
	if len(msgs) != 4 {
		t.Fatalf("messages = %+v, want prompt/reply/say/reply", msgs)
	}
	if msgs[0].Role != roleUser || msgs[0].Text != "启动 acp job 的消息。" || msgs[0].Injected != messagesRulesFrame {
		t.Fatalf("message 0 = %+v, want the stored prompt with its frame", msgs[0])
	}
	if msgs[1].Role != roleAgent || msgs[1].JobID != "job-acp" || msgs[1].Text != "第一条回复。" {
		t.Fatalf("message 1 = %+v, want the agent's first reply", msgs[1])
	}
	if msgs[2].Role != roleUser || msgs[2].Text != "say 问一句" {
		t.Fatalf("message 2 = %+v, want the say turn", msgs[2])
	}
	if msgs[3].Role != roleAgent || !msgs[3].Truncated {
		t.Fatalf("message 3 = %+v, want a truncated reply excerpt", msgs[3])
	}
	if len([]rune(msgs[3].Text)) != messagesReplyRunes {
		t.Fatalf("reply excerpt = %d runes, want %d", len([]rune(msgs[3].Text)), messagesReplyRunes)
	}
}

// TestJobDetailMessagesWindowGap: a resident session whose structured record outgrew
// the read window still shows the job's own prompt first (it comes from the request,
// not the record), marks the turn the window starts at, and never invents the turns
// in between.
func TestJobDetailMessagesWindowGap(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, messagesCallers())
	res := addMessageJob(t, s, "job-big", "alice", "", "首条消息", time.Now().Unix())
	filler := strings.Repeat("x", 4096)
	records := []map[string]any{
		{"t": "prompt", "text": "首条消息"},
		{"t": "message", "text": "第一条回复"},
	}
	for i := 0; i < 80; i++ {
		records = append(records,
			map[string]any{"t": "prompt", "text": fmt.Sprintf("第 %d 轮 %s", i, filler)},
			map[string]any{"t": "message", "text": "回复 " + filler},
		)
	}
	records = append(records,
		map[string]any{"t": "prompt", "text": "最后一轮"},
		map[string]any{"t": "message", "text": "最后的回复"},
	)
	writeMessageTranscript(t, res, records...)

	msgs := getJobMessages(t, s, "job-big", "tok-alice")
	if len(msgs) < 3 {
		t.Fatalf("messages = %d items, want the stored prompt + the windowed turns", len(msgs))
	}
	if msgs[0].Text != "首条消息" {
		t.Fatalf("message 0 = %+v, want the job's own prompt from its request", msgs[0])
	}
	if !msgs[1].Omitted {
		t.Fatalf("message 1 = %+v, want the window-start marker", msgs[1])
	}
	last := msgs[len(msgs)-1]
	if last.Role != roleAgent || last.Text != "最后的回复" {
		t.Fatalf("last message = %+v, want the newest reply", last)
	}
	if msgs[len(msgs)-2].Text != "最后一轮" {
		t.Fatalf("before last = %+v, want the newest say turn", msgs[len(msgs)-2])
	}
	for _, m := range msgs {
		if strings.HasPrefix(m.Text, "第一条回复") {
			t.Fatalf("the window served a turn from outside it: %+v", m)
		}
	}
}

// TestJobDetailMessagesRedacted: a redacted job shows the placeholder, and a secret
// that only the read path can spot never leaves the server.
func TestJobDetailMessagesRedacted(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, messagesCallers())
	now := time.Now().Unix()
	addMessageJob(t, s, "job-redacted", "alice", "", "请用 ***REDACTED*** 连接。", now)
	addMessageJob(t, s, "job-secret", "alice", "", "deploy with api_key: sk-live-abcdef123456", now)

	msgs := getJobMessages(t, s, "job-redacted", "tok-alice")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "***REDACTED***") {
		t.Fatalf("messages = %+v, want the redacted text", msgs)
	}

	msgs = getJobMessages(t, s, "job-secret", "tok-alice")
	if len(msgs) != 1 {
		t.Fatalf("messages = %+v, want one", msgs)
	}
	if strings.Contains(msgs[0].Text, "sk-live-abcdef123456") {
		t.Fatalf("text = %q, want the credential stripped on read", msgs[0].Text)
	}
	if !strings.Contains(msgs[0].Text, "api_key: ***REDACTED***") {
		t.Fatalf("text = %q, want the key kept with a placeholder value", msgs[0].Text)
	}
}

// TestJobDetailMessagesAccess: the list rides the job detail route's own read rule
// (job.read.others in its deny form), and a detail requested WITHOUT the include
// carries no messages key at all.
func TestJobDetailMessagesAccess(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, messagesCallers())
	addMessageJob(t, s, "job-private", "alice", "", "私密消息", time.Now().Unix())

	// No grants at all: the owner needs nothing, another caller is refused.
	s.setDBGrants(nil)
	resp := do(t, s, http.MethodGet, "/v1/jobs/job-private?include=messages", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("owner's status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, s, http.MethodGet, "/v1/jobs/job-private?include=messages", "tok-bob", nil)
	if resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("another caller's status = %d, want 403", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, authz.PermJobReadOthers) {
		t.Fatalf("403 body %q does not name %s", body, authz.PermJobReadOthers)
	}

	// job.read.others is exactly what opens it.
	s.setDBGrants(grantsFor([]string{authz.PermJobReadOthers}))
	resp = do(t, s, http.MethodGet, "/v1/jobs/job-private?include=messages", "tok-bob", nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status with job.read.others = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// A plain detail (no include) never learns the key exists.
	code, body := getInclude(t, s, "job-private", "", "tok-alice")
	if code != http.StatusOK {
		t.Fatalf("plain detail status = %d", code)
	}
	if _, ok := body["messages"]; ok {
		t.Fatalf("plain detail unexpectedly has messages: %v", body["messages"])
	}

	if code, _ := getInclude(t, s, "job-private", "?include=nope", "tok-alice"); code != http.StatusBadRequest {
		t.Fatalf("unknown include status = %d, want 400", code)
	}
}
