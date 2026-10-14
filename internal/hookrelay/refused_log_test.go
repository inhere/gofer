package hookrelay

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
)

// gofer-eh53, hook half: when the hub refuses a report because the session belongs
// to another caller (403), the terminal's hook log says so ONCE per window and
// names the fix — otherwise the only symptom is a web page that stopped moving.
func TestHeartbeatRefusalIsLoggedOncePerWindow(t *testing.T) {
	api := newFake()
	_, _ = api.RegisterSession(client.SessionRegister{SessionID: "refused-sid", Agent: AgentClaude})
	api.failHeartbeat = &client.StatusError{
		Status: 403,
		Msg:    "session heartbeat failed: sessionrelay: session heartbeat caller is not the registered owner",
	}

	var log bytes.Buffer
	now := time.Unix(1000, 0)
	p := Payload{Agent: AgentClaude, Event: "SessionEnd", SessionID: "refused-sid", Cwd: "/tmp"}
	opts := Options{Log: &log, RefusedStateDir: t.TempDir(), now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if _, err := Run(api, p, opts); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if n := strings.Count(log.String(), "report REFUSED"); n != 1 {
		t.Fatalf("three refused beats logged %d REFUSED lines, want 1:\n%s", n, log.String())
	}
	if !strings.Contains(log.String(), "gofer session owner") {
		t.Fatalf("the line does not name the fix:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "heartbeat failed") {
		t.Fatalf("the ordinary failure line was dropped:\n%s", log.String())
	}

	// Past the window the same session is reported again (the state was never fixed).
	now = now.Add(RefusedLogWindow + time.Minute)
	if _, err := Run(api, p, opts); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(log.String(), "report REFUSED"); n != 2 {
		t.Fatalf("after the window: %d REFUSED lines, want 2:\n%s", n, log.String())
	}

	// Another session is its own line, straight away.
	_, _ = api.RegisterSession(client.SessionRegister{SessionID: "refused-sid-2", Agent: AgentClaude})
	p2 := Payload{Agent: AgentClaude, Event: "SessionEnd", SessionID: "refused-sid-2", Cwd: "/tmp"}
	if _, err := Run(api, p2, opts); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(log.String(), "report REFUSED"); n != 3 {
		t.Fatalf("second session: %d REFUSED lines, want 3:\n%s", n, log.String())
	}

	// Every other failure is not this story: a 404 keeps its plain line only.
	log.Reset()
	api.failHeartbeat = &client.StatusError{Status: 404, Msg: "server 404: unknown session"}
	if _, err := Run(api, p2, opts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "report REFUSED") {
		t.Fatalf("a 404 was reported as an owner refusal:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "heartbeat failed") {
		t.Fatalf("the plain line is gone:\n%s", log.String())
	}
}

// The register path gets the same line (a hook that starts mid-session registers
// first), and so does the reply path through the same helper.
func TestRegisterRefusalIsLogged(t *testing.T) {
	api := newFake()
	_, _ = api.RegisterSession(client.SessionRegister{SessionID: "reg-refused", Agent: AgentClaude})
	api.failHeartbeat = &client.StatusError{Status: 403, Msg: "register session not permitted for this caller"}
	api.failRegister = &client.StatusError{Status: 403, Msg: "register session not permitted for this caller"}

	var log bytes.Buffer
	p := Payload{Agent: AgentClaude, Event: "SessionStart", SessionID: "reg-refused", Cwd: "/tmp"}
	opts := Options{Log: &log, RefusedStateDir: t.TempDir(), CurrentFile: t.TempDir() + "/cur"}
	if _, err := Run(api, p, opts); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(log.String(), "report REFUSED"); n != 1 {
		t.Fatalf("register refusal logged %d REFUSED lines, want 1:\n%s", n, log.String())
	}
}
