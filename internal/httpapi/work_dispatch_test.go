package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/wait"
	"github.com/inhere/gofer/internal/work"
)

// 收集箱分派（第一期收集箱设计「三、收集箱」）。权限反向用例在 authz_inhandler_test.go 的
// "POST /v1/work-items/{id}/dispatch" 用例里；这里测各分支的语义。

// capturedItem seeds one captured item (the resource of the dispatch reverse case).
func (e *authzCaseEnv) capturedItem() string {
	if e.capturedID == "" {
		w, err := e.s.jobs.Meta().CaptureWorkItem(jobstore.CaptureInput{Text: "case capture", Kind: "idea", ProjectKey: "self", By: e.owner})
		if err != nil {
			e.t.Fatalf("capture item for %s: %v", e.owner, err)
		}
		e.capturedID = w.ID
	}
	return e.capturedID
}

func newCaptured(t *testing.T, s *Server, text string) string {
	t.Helper()
	resp := captureReq(t, s, testToken, "", map[string]any{"text": text, "kind": "idea"})
	var d work.DetailView
	decode(t, resp, &d)
	if d.Status != jobstore.WorkCaptured {
		t.Fatalf("captured item status = %q", d.Status)
	}
	return d.ID
}

func dispatch(t *testing.T, s *Server, token, id string, body map[string]any) *http.Response {
	t.Helper()
	return do(t, s, http.MethodPost, "/v1/work-items/"+id+"/dispatch", token, body)
}

func itemDetail(t *testing.T, s *Server, id string) work.DetailView {
	t.Helper()
	var d work.DetailView
	decode(t, do(t, s, http.MethodGet, "/v1/work-items/"+id, testToken, nil), &d)
	return d
}

func journalText(t *testing.T, s *Server, id string) string {
	t.Helper()
	list, err := s.jobs.Meta().ListWorkJournal(id, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, e := range list {
		sb.WriteString(e.Text + "\n")
	}
	return sb.String()
}

func TestWorkDispatchToSession(t *testing.T) {
	s := newTestServer(t, testToken, false)
	s.relay.SetMessenger(&y6Messenger{})
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "disp-ok", "agent": "claude", "runner": "server", "project_key": "self", "event": "SessionStart",
		"peer_name": "inspect-22", "peer_status": "idle", "peer_messaging": true,
	})
	resp.Body.Close()
	id := newCaptured(t, s, "把导出做成异步")

	resp = dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-ok"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	d := itemDetail(t, s, id)
	if d.Status != jobstore.WorkActive || !strings.Contains(journalText(t, s, id), "已送出到会话 disp-ok") {
		t.Fatalf("item after dispatch = %q / journal %q", d.Status, journalText(t, s, id))
	}
	out, err := s.jobs.Meta().ReadSessionOutbox("disp-ok")
	if err != nil || len(out) == 0 || !strings.Contains(out[0].Text, "把导出做成异步") {
		t.Fatalf("outbox = %+v (%v)", out, err)
	}

	// Dispatching again is refused: the item left the capture box.
	resp = dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-ok"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second dispatch = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestWorkDispatchToSessionFailureKeepsCaptured(t *testing.T) {
	s := newTestServer(t, testToken, false)
	// No peer address and no messenger: the hand-over fails (409), the item stays captured.
	resp := do(t, s, http.MethodPost, "/v1/sessions", testToken, map[string]any{
		"session_id": "disp-fail", "agent": "claude", "event": "SessionStart",
	})
	resp.Body.Close()
	id := newCaptured(t, s, "送不出去的想法")
	resp = dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": "disp-fail"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("failed dispatch = %d, want 409: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
	if d := itemDetail(t, s, id); d.Status != jobstore.WorkCaptured {
		t.Fatalf("item status after failure = %q, want captured", d.Status)
	}
	if strings.Contains(journalText(t, s, id), "已送出") {
		t.Fatalf("journal claims a hand-over that failed: %q", journalText(t, s, id))
	}

	// Unknown session = 404, missing id = 400; both leave it captured.
	for body, want := range map[string]int{"nope": http.StatusNotFound, "": http.StatusBadRequest} {
		resp = dispatch(t, s, testToken, id, map[string]any{"to": "session", "session_id": body})
		if resp.StatusCode != want {
			t.Fatalf("session %q = %d, want %d", body, resp.StatusCode, want)
		}
		resp.Body.Close()
	}
	if d := itemDetail(t, s, id); d.Status != jobstore.WorkCaptured {
		t.Fatalf("item status = %q, want captured", d.Status)
	}
}

func TestWorkDispatchToTodo(t *testing.T) {
	s := newTestServer(t, testToken, false)
	id := newCaptured(t, s, "做成 todo 的想法")
	resp := dispatch(t, s, testToken, id, map[string]any{"to": "todo"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("to todo = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	d := itemDetail(t, s, id)
	if d.Status != jobstore.WorkActive {
		t.Fatalf("status = %q, want active", d.Status)
	}
	todoID, _, ok, err := s.work.ExistingTodo(id)
	if err != nil || !ok || todoID == "" {
		t.Fatalf("todo link missing: %q %v %v", todoID, ok, err)
	}
	if !strings.Contains(journalText(t, s, id), todoID) {
		t.Fatalf("journal lacks the todo: %q", journalText(t, s, id))
	}
	// Unknown plan = 404 and the item stays captured.
	id2 := newCaptured(t, s, "另一个")
	resp = dispatch(t, s, testToken, id2, map[string]any{"to": "todo", "plan_id": "plan-nope"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown plan = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	if d := itemDetail(t, s, id2); d.Status != jobstore.WorkCaptured {
		t.Fatalf("status = %q, want captured", d.Status)
	}
}

func TestWorkDispatchArchiveDropAndNotCaptured(t *testing.T) {
	s := newTestServer(t, testToken, false)
	a := newCaptured(t, s, "归档我")
	resp := dispatch(t, s, testToken, a, map[string]any{"to": "archive"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if d := itemDetail(t, s, a); d.Status != jobstore.WorkDone || !strings.Contains(journalText(t, s, a), "归档") {
		t.Fatalf("archived item = %q / %q", d.Status, journalText(t, s, a))
	}
	dr := newCaptured(t, s, "丢弃我")
	resp = dispatch(t, s, testToken, dr, map[string]any{"to": "drop"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drop = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if d := itemDetail(t, s, dr); d.Status != jobstore.WorkDropped {
		t.Fatalf("dropped item = %q", d.Status)
	}

	// Anything that is no longer captured is a 409 for every target; issue is not supported yet.
	for _, to := range []string{"archive", "drop", "todo"} {
		resp = dispatch(t, s, testToken, a, map[string]any{"to": to})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s on a non-captured item = %d, want 409", to, resp.StatusCode)
		}
		resp.Body.Close()
	}
	plain, err := s.jobs.Meta().CreateWorkItem(jobstore.WorkItemInput{Title: "board item", By: "t"})
	if err != nil {
		t.Fatal(err)
	}
	resp = dispatch(t, s, testToken, plain.ID, map[string]any{"to": "archive"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("board item = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
	c := newCaptured(t, s, "issue 目标")
	for body, want := range map[string]int{"issue": http.StatusBadRequest, "bogus": http.StatusBadRequest, "": http.StatusBadRequest} {
		resp = dispatch(t, s, testToken, c, map[string]any{"to": body})
		if resp.StatusCode != want {
			t.Fatalf("to=%q = %d, want %d", body, resp.StatusCode, want)
		}
		resp.Body.Close()
	}
	resp = dispatch(t, s, testToken, "w-nope", map[string]any{"to": "archive"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown item = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	if d := itemDetail(t, s, c); d.Status != jobstore.WorkCaptured {
		t.Fatalf("status = %q, want captured", d.Status)
	}
}

func submitDispatchJob(t *testing.T, s *Server, title string) job.JobResult {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: []string{"go", "version"}, Cwd: ".", Title: title,
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var r job.JobResult
	decode(t, resp, &r)
	return r
}

func TestWorkDispatchToJobWritesTheOutcomeBack(t *testing.T) {
	s := newTestServer(t, testToken, false)

	// Linked right after the submit: whether the job is still running or already ended, the
	// terminal status ends up on the item's journal.
	id := newCaptured(t, s, "让 job 去做")
	j := submitDispatchJob(t, s, "dispatch e2e")
	resp := dispatch(t, s, testToken, id, map[string]any{"to": "job", "job_id": j.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("to job = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if d := itemDetail(t, s, id); d.Status != jobstore.WorkActive {
		t.Fatalf("status = %q, want active", d.Status)
	}
	links, err := s.jobs.Meta().ListWorkLinks(id)
	if err != nil || len(links) != 1 || links[0].Kind != jobstore.WorkLinkJob || links[0].Ref != j.ID {
		t.Fatalf("links = %+v (%v)", links, err)
	}
	waitDone(t, s, j.ID)
	s.jobs.Wait(j.ID)
	want := "job " + j.ID[:min(len(j.ID), 8)]
	wait.Until(t, wait.Timeout(t, 10*time.Second), "the job outcome on the item journal", func() bool {
		txt := journalText(t, s, id)
		return strings.Contains(txt, "完成") && strings.Contains(txt, want[:5])
	})

	// A job that ended BEFORE it was linked still gets its outcome written, once linked.
	id2 := newCaptured(t, s, "已经结束的 job")
	j2 := submitDispatchJob(t, s, "dispatch ended")
	waitDone(t, s, j2.ID)
	s.jobs.Wait(j2.ID)
	resp = dispatch(t, s, testToken, id2, map[string]any{"to": "job", "job_id": j2.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("to ended job = %d: %s", resp.StatusCode, readBody(t, resp))
	}
	if !strings.Contains(journalText(t, s, id2), "完成") {
		t.Fatalf("journal lacks the outcome of the already-ended job: %q", journalText(t, s, id2))
	}

	// Unknown job = 404, missing job_id = 400; the item stays captured.
	id3 := newCaptured(t, s, "没有 job")
	for body, want := range map[string]int{"job-nope": http.StatusNotFound, "": http.StatusBadRequest} {
		resp = dispatch(t, s, testToken, id3, map[string]any{"to": "job", "job_id": body})
		if resp.StatusCode != want {
			t.Fatalf("job %q = %d, want %d", body, resp.StatusCode, want)
		}
		resp.Body.Close()
	}
	if d := itemDetail(t, s, id3); d.Status != jobstore.WorkCaptured {
		t.Fatalf("status = %q, want captured", d.Status)
	}
}

func TestWorkDispatchRefusesWorkerAndJobCredential(t *testing.T) {
	s := topicServer(t)
	id := captureItem(t, s, "tok-alice", "谁都不能代分派", "")
	jobTok := seedJobToken(t, s, "job-disp", jobstore.JobCredentialMember, "")
	for _, tok := range []string{"tok-worker", jobTok} {
		resp := dispatch(t, s, tok, id, map[string]any{"to": "drop"})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("token %s = %d, want 403", tok, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if w, _, _ := s.jobs.Meta().GetWorkItem(id); w.Status != jobstore.WorkCaptured {
		t.Fatalf("status = %q, want captured", w.Status)
	}
}
