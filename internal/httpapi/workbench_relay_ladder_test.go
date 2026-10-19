package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// A workbench turn to a terminal session uses the drawer's delivery ladder: a RUNNING
// session (no open turn) with a tmux pane is typed into, and the response says how.
func TestWorkbenchRelayTurnUsesDeliveryLadder(t *testing.T) {
	f := newWBAttFixture(t, 0)
	inj := &fakeInjector{res: sessionrelay.InjectResult{JobID: "job-wb-inject"}}
	f.s.relay.SetInjector(inj)
	f.runningSession(t, "relay-run", "%3")

	status, out := f.turn(t, "r:relay-run", map[string]any{"text": "carry on"})
	if status != http.StatusOK || out["path"] != "tmux" || out["job_id"] != "job-wb-inject" || out["decision_id"] == nil {
		t.Fatalf("turn = %d %v, want 200 path=tmux with the job and audit ids", status, out)
	}
	if len(inj.reqs) != 1 || !strings.Contains(strings.Join(inj.reqs[0].Cmd, " "), "carry on") {
		t.Fatalf("injection = %+v", inj.reqs)
	}
}

// With images, the file is placed, the message goes out the ladder carrying the absolute
// path, the attachment is bound to the audit row, and the session history shows it.
func TestWorkbenchRelayTurnWithAttachmentsOnLadder(t *testing.T) {
	f := newWBAttFixture(t, 0)
	inj := &fakeInjector{res: sessionrelay.InjectResult{JobID: "job-wb-inject2"}}
	f.s.relay.SetInjector(inj)
	f.runningSession(t, "relay-run2", "%4")
	id := f.save(t, "default")

	status, out := f.turn(t, "r:relay-run2", map[string]any{"text": "see", "attachments": []string{id}})
	if status != http.StatusOK || out["path"] != "tmux" {
		t.Fatalf("turn = %d %v", status, out)
	}
	decisionID := out["decision_id"].(string)
	matches, _ := filepath.Glob(filepath.Join(f.root, "tmp", "gofer-attachments", "*", id+".png"))
	if len(matches) != 1 {
		t.Fatalf("placed file not found under the project: %v", matches)
	}
	if _, err := os.Stat(matches[0]); err != nil {
		t.Fatal(err)
	}
	// One line: "see [附图 1] <path>" — a newline would be typed as a separate Enter.
	if len(inj.reqs) != 1 || !strings.Contains(strings.Join(inj.reqs[0].Cmd, " "), "-l -- '[gofer web 回复] see [附图 1] "+matches[0]+"'") {
		t.Fatalf("typed text lacks the one-line image path %q: %+v", matches[0], inj.reqs)
	}
	rec, _, _ := f.s.jobs.Meta().GetAttachment(id)
	if rec.ThreadID != "r:relay-run2" || rec.RefKind != "decision" || rec.RefID != decisionID {
		t.Fatalf("attachment binding = %+v, want the audit row %s", rec, decisionID)
	}
	resp := do(t, f.s, http.MethodGet, "/v1/sessions/relay-run2", testToken, nil)
	var detail struct {
		Turns []struct {
			ID          string              `json:"id"`
			Attachments []attachmentRefView `json:"attachments"`
		} `json:"turns"`
	}
	decode(t, resp, &detail)
	for _, turn := range detail.Turns {
		if turn.ID == decisionID && len(turn.Attachments) == 1 && turn.Attachments[0].ID == id {
			return
		}
	}
	t.Fatalf("history lacks the attachment on %s: %+v", decisionID, detail.Turns)
}

// A session that cannot be reached fails BEFORE any file is placed, with a human reason.
func TestWorkbenchRelayTurnUndeliverableSendsAndPlacesNothing(t *testing.T) {
	f := newWBAttFixture(t, 0)
	inj := &fakeInjector{}
	f.s.relay.SetInjector(inj)
	f.runningSession(t, "relay-nopane", "") // running, no open turn, no tmux pane
	id := f.save(t, "default")

	status, out := f.turn(t, "r:relay-nopane", map[string]any{"text": "hi", "attachments": []string{id}})
	detail, _ := out["detail"].(string)
	if status != http.StatusConflict || !strings.Contains(detail, "会话当前无法接收消息") || strings.Contains(detail, "no open turn") {
		t.Fatalf("turn = %d %v, want 409 with the friendly reason", status, out)
	}
	if _, err := os.Stat(filepath.Join(f.root, "tmp", "gofer-attachments")); !os.IsNotExist(err) {
		t.Fatalf("a file was placed for an undeliverable session (err=%v)", err)
	}
	if rec, _, _ := f.s.jobs.Meta().GetAttachment(id); rec.ThreadID != "" {
		t.Fatalf("attachment bound: %+v", rec)
	}
	if len(inj.reqs) != 0 {
		t.Fatalf("injected: %+v", inj.reqs)
	}
	// Without images the same session gives the same friendly 409.
	status, out = f.turn(t, "r:relay-nopane", map[string]any{"text": "hi"})
	if detail, _ := out["detail"].(string); status != http.StatusConflict || !strings.Contains(detail, "会话当前无法接收消息") {
		t.Fatalf("text-only turn = %d %v", status, out)
	}
}

func (f *wbAttFixture) runningSession(t *testing.T, sid, pane string) {
	t.Helper()
	if _, err := f.s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: sid, Agent: "claude", ProjectKey: "self", Runner: "local", CallerID: "default", TmuxPane: pane,
		State: jobstore.SessionRunning, RelayMode: "on", StartedAt: f.now - 30, LastSeenAt: f.now - 5,
		Cwd: f.root,
	}); err != nil {
		t.Fatal(err)
	}
}

type recordingMessenger struct{ commands [][]string }

func (m *recordingMessenger) SubmitMessenger(_, _, _ string, command []string, _, _ string) (string, error) {
	m.commands = append(m.commands, command)
	return "msgr-job", nil
}
func (*recordingMessenger) MessengerJob(string) (bool, string, int, string, error) {
	return true, "done", 0, "已发送", nil
}

// A session reachable ONLY through the messenger (a Claude Code session with a SendMessage
// address, no tmux, no deliver_command) receives the turn the way the drawer's message does:
// the files are placed, the one-line image paths ride in the forwarded text, the attachment is
// bound to the outbox message, and the drawer's outbox history shows it.
func TestWorkbenchRelayTurnViaMessengerWithAttachments(t *testing.T) {
	f := newWBAttFixture(t, 0)
	msgr := &recordingMessenger{}
	f.s.relay.SetMessenger(msgr)
	if _, err := f.s.jobs.Meta().UpsertAgentSession(jobstore.AgentSession{
		SessionID: "relay-peer", Agent: "claude", ProjectKey: "self", Runner: "local", CallerID: "default",
		State: jobstore.SessionRunning, RelayMode: "on", StartedAt: f.now - 30, LastSeenAt: f.now - 5, Cwd: f.root,
		PeerName: "inspect-9", PeerMessaging: true,
	}); err != nil {
		t.Fatal(err)
	}
	id := f.save(t, "default")

	status, out := f.turn(t, "r:relay-peer", map[string]any{"text": "look", "attachments": []string{id}})
	if status != http.StatusOK || out["path"] != "messenger" || out["message_id"] == nil {
		t.Fatalf("turn = %d %v, want 200 via the messenger with a message id", status, out)
	}
	matches, _ := filepath.Glob(filepath.Join(f.root, "tmp", "gofer-attachments", "*", id+".png"))
	if len(matches) != 1 {
		t.Fatalf("placed file not found: %v", matches)
	}
	if len(msgr.commands) != 1 || !strings.Contains(strings.Join(msgr.commands[0], " "), "look [附图 1] "+matches[0]) {
		t.Fatalf("messenger text lacks the one-line image path: %v", msgr.commands)
	}
	msgID := out["message_id"].(string)
	rec, _, _ := f.s.jobs.Meta().GetAttachment(id)
	if rec.ThreadID != "r:relay-peer" || rec.RefKind != "message" || rec.RefID != msgID {
		t.Fatalf("attachment binding = %+v, want the outbox message %s", rec, msgID)
	}
	resp := do(t, f.s, http.MethodGet, "/v1/sessions/relay-peer/outbox", testToken, nil)
	var box struct {
		Messages []struct {
			ID          string              `json:"id"`
			Attachments []attachmentRefView `json:"attachments"`
		} `json:"messages"`
	}
	decode(t, resp, &box)
	for _, m := range box.Messages {
		if m.ID == msgID && len(m.Attachments) == 1 && m.Attachments[0].ID == id {
			return
		}
	}
	t.Fatalf("outbox history lacks the attachment on %s: %+v", msgID, box.Messages)
}
