package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/attachment"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/xfer"
)

// wbAttFixture is a workbench server with the attachment store and a LOCAL transfer
// manager wired, plus one terminal (relay) session and one ended CLI session.
type wbAttFixture struct {
	s    *Server
	svc  *attachment.Service
	root string
	now  int64
}

func newWBAttFixture(t *testing.T, maxPerMessage int) *wbAttFixture {
	t.Helper()
	s := newWorkbenchTestServer(t, config.ServerConfig{
		Token: testToken, Attachments: config.AttachmentsConfig{MaxPerMessage: maxPerMessage},
	})
	svc := attachment.New(t.TempDir(), s.jobs.Meta(), func() attachment.Limits {
		return attachment.Limits{MaxBytes: 1 << 20, HourlyPerCaller: 100, Retention: 24 * time.Hour}
	})
	mgr, err := xfer.NewManager(xfer.Options{Root: t.TempDir(), Repo: s.jobs.Meta()})
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetRunner(&xfer.LocalRunner{Config: s.jobs.Config, Store: mgr.Store(), OnGetContent: mgr.CommitGet})
	s.SetXfer(mgr)
	s.SetAttachments(svc)
	f := &wbAttFixture{s: s, svc: svc, root: s.jobs.Config().Projects["self"].HostPath, now: time.Now().Unix()}
	seedWorkbenchRelay(t, s, "relay-att", "self", jobstore.SessionWaitingReply, f.now-100, f.now-20)
	f.openTurn(t, "relay-att")
	return f
}

func (f *wbAttFixture) openTurn(t *testing.T, sid string) {
	t.Helper()
	d := jobstore.PlanDecision{Title: "relay", Question: "reply", State: jobstore.DecisionOpen, SessionID: sid,
		Kind: jobstore.DecisionKindRelay, AskedAt: f.now - 20, TimeoutSec: 3600}
	if err := f.s.jobs.Meta().InsertDecision(&d); err != nil {
		t.Fatal(err)
	}
}

func (f *wbAttFixture) save(t *testing.T, caller string) string {
	t.Helper()
	rec, err := f.svc.Save(caller, bytes.NewReader(testPNG(t, 24, 16)))
	if err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

func (f *wbAttFixture) turn(t *testing.T, thread string, body any) (int, map[string]any) {
	t.Helper()
	resp := do(t, f.s, http.MethodPost, "/v1/workbench/threads/"+thread+"/turn", testToken, body)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

func TestWorkbenchTurnAttachmentsRelayFileDeliveryAndHistory(t *testing.T) {
	f := newWBAttFixture(t, 0)
	id := f.save(t, "default")

	status, out := f.turn(t, "r:relay-att", map[string]any{"text": "see this", "attachments": []string{id}})
	if status != http.StatusOK || out["decision_id"] == nil {
		t.Fatalf("turn = %d %v", status, out)
	}
	decisionID := out["decision_id"].(string)
	d, ok, err := f.s.jobs.Meta().GetDecision(decisionID)
	if err != nil || !ok {
		t.Fatalf("GetDecision: ok=%v err=%v", ok, err)
	}
	if !strings.HasPrefix(d.Answer, "see this [附图 1] ") {
		t.Fatalf("answer = %q", d.Answer)
	}
	abs := strings.TrimPrefix(d.Answer, "see this [附图 1] ")
	if !filepath.IsAbs(abs) || !strings.HasPrefix(abs, f.root) {
		t.Fatalf("abs path %q not under project root %q", abs, f.root)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("placed file missing: %v", err)
	}

	// History: the session's turn carries the attachment reference.
	resp := do(t, f.s, http.MethodGet, "/v1/sessions/relay-att", testToken, nil)
	var detail struct {
		Turns []struct {
			ID          string `json:"id"`
			Attachments []struct {
				ID     string `json:"id"`
				Mime   string `json:"mime"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"attachments"`
		} `json:"turns"`
	}
	decode(t, resp, &detail)
	found := false
	for _, turn := range detail.Turns {
		if turn.ID == decisionID {
			found = true
			if len(turn.Attachments) != 1 || turn.Attachments[0].ID != id || turn.Attachments[0].Mime != "image/png" || turn.Attachments[0].Width != 24 {
				t.Fatalf("turn attachments = %+v", turn.Attachments)
			}
		}
	}
	if !found {
		t.Fatalf("decision %s not in session turns: %+v", decisionID, detail.Turns)
	}

	// The same attachment cannot ride a second message.
	f.openTurn(t, "relay-att")
	if status, _ := f.turn(t, "r:relay-att", map[string]any{"text": "again", "attachments": []string{id}}); status != http.StatusConflict {
		t.Fatalf("resend = %d, want 409", status)
	}
}

func TestWorkbenchTurnAttachmentValidation(t *testing.T) {
	f := newWBAttFixture(t, 1)
	mine, other := f.save(t, "default"), f.save(t, "someone-else")
	extra := f.save(t, "default")

	if status, _ := f.turn(t, "r:relay-att", map[string]any{"text": "x", "attachments": []string{other}}); status != http.StatusForbidden {
		t.Fatalf("foreign attachment = %d, want 403", status)
	}
	if status, _ := f.turn(t, "r:relay-att", map[string]any{"text": "x", "attachments": []string{mine, extra}}); status != http.StatusBadRequest {
		t.Fatalf("over max_per_message = %d, want 400", status)
	}
	// Nothing above reached the session or consumed the attachment.
	if rec, _, _ := f.s.jobs.Meta().GetAttachment(mine); rec.ThreadID != "" {
		t.Fatalf("a refused send bound the attachment: %+v", rec)
	}
}

func TestWorkbenchTurnAttachmentsRefusedForAgentWithoutImages(t *testing.T) {
	f := newWBAttFixture(t, 0)
	if _, err := f.s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "relay-suga", Agent: "suga", ProjectKey: "self", Runner: "local", CallerID: "default",
		State: jobstore.SessionWaitingReply, RelayMode: "on", StartedAt: f.now - 10, LastSeenAt: f.now - 5,
	}); err != nil {
		t.Fatal(err)
	}
	f.openTurn(t, "relay-suga")
	id := f.save(t, "default")
	status, out := f.turn(t, "r:relay-suga", map[string]any{"text": "x", "attachments": []string{id}})
	if status != http.StatusBadRequest || !strings.Contains(out["detail"].(string), "suga 暂不支持读取图片") {
		t.Fatalf("turn = %d %v, want 400 naming the reason", status, out)
	}
	if rec, _, _ := f.s.jobs.Meta().GetAttachment(id); rec.ThreadID != "" {
		t.Fatalf("attachment bound by a refused send: %+v", rec)
	}
}

func TestWorkbenchThreadsReportAttachmentCapability(t *testing.T) {
	f := newWBAttFixture(t, 0)
	if _, err := f.s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "relay-suga2", Agent: "suga", ProjectKey: "self", Runner: "local", CallerID: "default",
		State: jobstore.SessionRunning, RelayMode: "on", StartedAt: f.now - 10, LastSeenAt: f.now - 5,
	}); err != nil {
		t.Fatal(err)
	}
	resp := do(t, f.s, http.MethodGet, "/v1/workbench/threads?since=1", testToken, nil)
	var got struct {
		Projects []struct {
			Threads []struct {
				ID                string `json:"id"`
				Attachments       string `json:"attachments"`
				AttachmentsReason string `json:"attachments_reason"`
			} `json:"threads"`
		} `json:"projects"`
	}
	decode(t, resp, &got)
	seen := map[string][2]string{}
	for _, p := range got.Projects {
		for _, th := range p.Threads {
			seen[th.ID] = [2]string{th.Attachments, th.AttachmentsReason}
		}
	}
	if seen["r:relay-att"] != [2]string{"file", ""} {
		t.Fatalf("claude terminal session = %v", seen["r:relay-att"])
	}
	if seen["r:relay-suga2"] != [2]string{"none", "suga 暂不支持读取图片"} {
		t.Fatalf("suga terminal session = %v", seen["r:relay-suga2"])
	}
}

// The job conversation list joins the n-th user message of a job to the attachments bound to
// its n-th prompt.
func TestJobMessagesCarryPromptAttachments(t *testing.T) {
	f := newWBAttFixture(t, 0)
	seedWorkbenchJob(t, f.s, jobstore.JobRecord{ID: "conv-job", Agent: "cli", SessionID: "conv-sid", Status: job.StatusDone, StartedAt: f.now - 50}, "conv", "hello")
	id := f.save(t, "default")
	if err := f.s.jobs.Meta().BindAttachment(id, "s:conv-sid", "prompt", "conv-job#1"); err != nil {
		t.Fatal(err)
	}
	res, ok := f.s.jobs.Get("conv-job")
	if !ok {
		list, _ := f.s.jobs.ListJobs(job.ListOpts{})
		for _, j := range list {
			if j.ID == "conv-job" {
				res, ok = j, true
			}
		}
	}
	if !ok {
		t.Fatal("seeded job not found")
	}
	var userMsgs []jobMessageView
	for _, m := range f.s.jobMessages(res) {
		if m.Role == roleUser {
			userMsgs = append(userMsgs, m)
		}
	}
	if len(userMsgs) != 1 || len(userMsgs[0].Attachments) != 1 || userMsgs[0].Attachments[0].ID != id {
		t.Fatalf("user messages = %+v", userMsgs)
	}
}

// promptAttachments is the closure the ACP stream uses: it returns the views of the matching
// prompt only, and marks an expired attachment.
func TestPromptAttachmentsClosure(t *testing.T) {
	f := newWBAttFixture(t, 0)
	a, b := f.save(t, "default"), f.save(t, "default")
	meta := f.s.jobs.Meta()
	if err := meta.BindAttachment(a, "s:sid-x", "prompt", "job-x#1"); err != nil {
		t.Fatal(err)
	}
	if err := meta.BindAttachment(b, "s:sid-x", "prompt", "job-x#2"); err != nil {
		t.Fatal(err)
	}
	pa := f.s.promptAttachments("job-x", "sid-x")
	if pa == nil {
		t.Fatal("closure is nil with the store wired")
	}
	if v, ok := pa(1).([]attachmentRefView); !ok || len(v) != 1 || v[0].ID != a || v[0].Expired {
		t.Fatalf("prompt 1 = %#v", pa(1))
	}
	if pa(3) != nil {
		t.Fatalf("prompt 3 = %#v, want nil", pa(3))
	}
	exp := attachmentRefViews([]jobstore.Attachment{{ID: "e", Mime: "image/png", Width: 1, ExpiresAt: 10}}, 20)
	if len(exp) != 1 || !exp[0].Expired || exp[0].Mime != "" {
		t.Fatalf("expired view = %+v", exp)
	}
}
