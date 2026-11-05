package messenger

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/testutil/wait"
)

func TestResidentMessengerPackageParity(t *testing.T) {
	m := New("claude", time.Minute)
	if got := m.Status("local"); got != "stopped" {
		t.Fatalf("new resident messenger status = %q, want stopped", got)
	}
	if _, err := m.Send(context.Background(), "remote", "", "t", []string{"claude", "-p", "hello"}); err == nil {
		t.Fatal("remote runner should be rejected before starting a process")
	}
}

func TestResidentMessengerSendsStreamJSON(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")
	got, err := m.Send(context.Background(), "local", "", "t", command)
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "已发送" {
		t.Fatalf("resident output = %q, want 已发送", got.Output)
	}
	if !got.Sent || got.Receipt() != "sent" {
		t.Fatalf("result = %+v, want a successful send (receipt %q)", got, got.Receipt())
	}
}

func TestResidentMessengerInjectsMessengerMarker(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON_ENV", "GOFER_MESSENGER")
	m := New("", time.Minute)
	command := append(testcmd.Cmd(t, "stream-json-env"), "-p", "hello")
	got, err := m.Send(context.Background(), "local", "", "t", command)
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "GOFER_MESSENGER=1" {
		t.Fatalf("resident child env = %q, want GOFER_MESSENGER=1", got.Output)
	}
	if !got.Sent {
		t.Fatalf("result = %+v, want a successful send", got)
	}
}

// sendResultFrame is the turn's own result frame — the one the reader hands to the
// request in flight. The captures in testdata stop after the tool_result (they are the
// raw frames T0 froze), so the parse tests append it.
const sendResultFrame = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`

// readTurn runs the real stdout reader over frames and returns the event the request in
// flight receives.
func readTurn(t *testing.T, frames ...string) event {
	t.Helper()
	p := &process{events: make(chan event, 1), done: make(chan error, 1), exited: make(chan struct{})}
	p.waiting.Store(true)
	p.read(strings.NewReader(strings.Join(frames, "\n") + "\n"))
	// read is synchronous: once it returns, the event is either queued or was never
	// produced (an unsolicited turn, or a stream that ended first).
	select {
	case ev := <-p.events:
		return ev
	default:
	}
	t.Fatalf("the reader produced no result event (it ended with %v)", <-p.done)
	return event{}
}

// captureFrames returns the lines of one sanitized frame capture in testdata.
func captureFrames(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// TestSendMessageResultFromRealFrames pins the parsing rule on the frames Claude Code
// 2.1.318 and 2.1.292 really produced (T0): the tool_result text is a JSON object whose
// success decides the delivery, and is_error is absent even when it failed.
func TestSendMessageResultFromRealFrames(t *testing.T) {
	tests := []struct {
		capture   string
		sent      bool
		reasonHas string
	}{
		{"linux-2.1.318-success-uds.jsonl", true, ""},
		{"win-2.1.292-success-uds.jsonl", true, ""},
		{"linux-2.1.318-success-name.jsonl", true, ""},
		{"win-2.1.292-success-name.jsonl", true, ""},
		{"linux-2.1.318-dupname.jsonl", false, "2 agents are named"},
		{"win-2.1.292-dupname.jsonl", false, "2 agents are named"},
		{"linux-2.1.318-never-existed.jsonl", false, "ENOENT"},
		{"win-2.1.292-never-existed.jsonl", false, "ENOINBOX: no-key"},
		{"linux-2.1.318-hard-killed.jsonl", false, "ECONNREFUSED"},
		{"win-2.1.292-hard-killed.jsonl", false, "ENOINBOX: dead-owner"},
		{"linux-2.1.318-noname.jsonl", false, "No agent named"},
		{"win-2.1.292-noname.jsonl", false, "No agent named"},
	}
	for _, tc := range tests {
		t.Run(tc.capture, func(t *testing.T) {
			ev := readTurn(t, append(captureFrames(t, tc.capture), sendResultFrame)...)
			if ev.sent != tc.sent {
				t.Fatalf("sent = %v, want %v (reason %q)", ev.sent, tc.sent, ev.reason)
			}
			if tc.reasonHas == "" {
				if ev.reason != "" {
					t.Fatalf("reason = %q, want none on success", ev.reason)
				}
				return
			}
			if !strings.Contains(ev.reason, tc.reasonHas) {
				t.Fatalf("reason = %q, want it to contain %q", ev.reason, tc.reasonHas)
			}
		})
	}
}

// TestSendMessageResultLastCallWins: a turn that calls the tool twice is judged on its
// last call (T0 froze that order: the retry is the one that counts).
func TestSendMessageResultLastCallWins(t *testing.T) {
	frames := append(captureFrames(t, "linux-2.1.318-success-uds.jsonl"), captureFrames(t, "linux-2.1.318-dupname.jsonl")...)
	ev := readTurn(t, append(frames, sendResultFrame)...)
	if ev.sent || !strings.Contains(ev.reason, "2 agents are named") {
		t.Fatalf("event = %+v, want the last call's failure", ev)
	}
}

// TestSendMessageResultPeerTurnIgnored pins R7: a call inside a peer turn answers a
// message another session sent to this process, so it must not be read as this
// request's outcome.
func TestSendMessageResultPeerTurnIgnored(t *testing.T) {
	frames := []string{`{"type":"command_lifecycle","state":"started"}`}
	frames = append(frames, captureFrames(t, "win-2.1.292-success-uds.jsonl")...)
	frames = append(frames, `{"type":"command_lifecycle","state":"completed"}`, sendResultFrame)
	ev := readTurn(t, frames...)
	if ev.sent || ev.reason != "" {
		t.Fatalf("event = %+v, want no SendMessage call recorded for the request", ev)
	}
	res := SendResult{Output: ev.output, Sent: ev.sent, Reason: ev.reason}
	if got := res.Receipt(); got != "send failed: no SendMessage call" {
		t.Fatalf("receipt = %q, want the no-call failure receipt", got)
	}
}

// TestSendMessageResultMissingSuccess: a tool_result gofer cannot read is a FAILURE
// (R5) — a wrong guess would show a lost message as delivered.
func TestSendMessageResultMissingSuccess(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		sent  bool
	}{
		{"no call at all", []string{sendResultFrame}, false},
		{"unreadable result", []string{
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"SendMessage","input":{"to":"proj-a","message":"hi"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"not the JSON object"}]}]},"tool_use_result":{"display":"Not sent"}}`,
			sendResultFrame,
		}, false},
		{"same name's tool_use_result", []string{
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"SendMessage","input":{"to":"proj-a","message":"hi"}}]}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"plain text result"}]},"tool_use_result":{"success":true,"message":"in that session's inbox"}}`,
			sendResultFrame,
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := readTurn(t, tc.lines...)
			if ev.sent != tc.sent {
				t.Fatalf("sent = %v (reason %q), want %v", ev.sent, ev.reason, tc.sent)
			}
			if !tc.sent && tc.name == "unreadable result" && ev.reason != reasonUnrecognizedResult {
				t.Fatalf("reason = %q, want %q", ev.reason, reasonUnrecognizedResult)
			}
		})
	}
}

// TestResidentMessengerReportsToolFailure: a tool-level failure is a RESULT (err == nil)
// and the delivery history records it as a failure, so the Runners drawer does not show
// a lost message as delivered.
func TestResidentMessengerReportsToolFailure(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	t.Setenv("GOFER_TEST_STREAM_JSON_FAIL", "Failed to send to uds:/tmp/cc-socks/1.sock: ENOENT")
	m := New("", time.Minute)
	t.Cleanup(m.Close)
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")
	got, err := m.Send(context.Background(), "local", "", "proj-a", command)
	if err != nil {
		t.Fatalf("send = %v, want the tool failure as a result value", err)
	}
	if got.Sent || !strings.Contains(got.Reason, "ENOENT") {
		t.Fatalf("result = %+v, want not sent with the tool's reason", got)
	}
	if want := "send failed: " + got.Reason; got.Receipt() != want {
		t.Fatalf("receipt = %q, want %q", got.Receipt(), want)
	}
	deliveries := m.Snapshot("local").Deliveries
	if len(deliveries) != 1 || deliveries[0].OK || !strings.Contains(deliveries[0].Error, "ENOENT") {
		t.Fatalf("deliveries = %+v, want one failed entry with the tool's reason", deliveries)
	}
}

func TestResidentMessengerRespawnsAfterIdleExit(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")
	if _, err := m.Send(context.Background(), "local", "", "t", command); err != nil {
		t.Fatal(err)
	}
	p, err := m.process("local", "", command)
	if err != nil {
		t.Fatal(err)
	}
	p.stop()
	if _, err := m.Send(context.Background(), "local", "", "t", command); err != nil {
		t.Fatalf("send after idle exit = %v, want respawn and retry", err)
	}
}

// TestResidentMessengerIdleTimerPausedDuringRequest pins gofer-r7am C3: the idle
// timer used to keep running while a request was in flight, so a reply slower than
// the idle window got the process killed mid-request ("resident messenger exited:
// stream closed"). The fake process answers 10x slower than the idle window; it
// behaves like the real one when killed (stdout ends, the reader reports done).
func TestResidentMessengerIdleTimerPausedDuringRequest(t *testing.T) {
	const idle = 30 * time.Millisecond
	m := New("", idle)
	p := newSlowFakeProcess(t, idle, 10*idle)
	m.processes["local"] = p
	p.touch() // armed by the previous request, as for any reused process

	got, err := m.Send(context.Background(), "local", "", "t", []string{"unused", "-p", "hello"})
	if err != nil {
		t.Fatalf("send with a reply slower than the idle window = %v, want the reply", err)
	}
	if got.Output != "slow reply" {
		t.Fatalf("output = %q, want slow reply", got.Output)
	}
	// Answered: the timer runs again and the idle process exits on its own.
	wait.Until(t, 5*time.Second, "idle process stopped after the reply", p.isStopped)
}

// newSlowFakeProcess builds a process whose "child" answers each request line after
// delay, and on stop ends like a killed child (done + exited).
func newSlowFakeProcess(t *testing.T, idle, delay time.Duration) *process {
	t.Helper()
	inR, inW := io.Pipe()
	p := &process{cmd: &exec.Cmd{}, stdin: inW, events: make(chan event, 1), done: make(chan error, 1),
		exited: make(chan struct{}), stopped: make(chan struct{}), idle: idle}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer close(p.exited)
		lines := bufio.NewScanner(inR)
		for lines.Scan() {
			select {
			case <-time.After(delay):
				p.events <- event{output: "slow reply"}
			case <-p.stopped:
				p.done <- errors.New("stream closed")
				return
			}
		}
		p.done <- errors.New("stream closed")
	}()
	t.Cleanup(func() {
		p.stop()
		<-finished
	})
	return p
}
