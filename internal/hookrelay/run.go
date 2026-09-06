package hookrelay

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/client"
)

// API is the hub surface the runner needs; *client.Client satisfies it.
type API interface {
	RegisterSession(in client.SessionRegister) (client.AgentSession, error)
	HeartbeatSession(sid string, hb client.SessionHeartbeat) (client.AgentSession, error)
	OpenSessionTurn(sid, msg string, timeoutSec int64) (client.Decision, error)
	WaitSessionTurn(sid, decisionID string, waitSec int) (client.TurnStatus, error)
	SetSessionRelay(sid string, on bool) (client.AgentSession, error)
}

// Options tunes one hook invocation. Zero values pick the defaults below.
type Options struct {
	// Runner labels where the session lives (server | <worker-id>).
	Runner string
	// ProjectKey overrides the server's cwd→project match (env GOFER_PROJECT).
	ProjectKey string
	// TmuxPane is $TMUX_PANE when the session runs inside tmux (optional).
	TmuxPane string
	// Wait is the Stop hook's total blocking budget (the hook's own timeout
	// minus a margin). Default 540s (the Claude default hook timeout is 600s).
	Wait time.Duration
	// PollSec is the per-request long-poll window (server caps at 25).
	PollSec int
	// MaxMessage caps the relayed last message (bytes). Default 4000.
	MaxMessage int
	// CurrentFile, when set, receives the session id on SessionStart so the CLI
	// can resolve "the session in this directory" offline.
	CurrentFile string
	// Log receives one line per notable step (nil = discard). Never stderr:
	// Claude Code shows hook stderr to the user.
	Log io.Writer

	now   func() time.Time
	sleep func(time.Duration)
}

// Result is what the hook must do after Run: when Blocked, print the
// `{"decision":"block","reason":Reason}` continuation and exit 0.
type Result struct {
	Blocked bool
	Reason  string
}

// ReplyPrefix marks an injected web reply so the model knows the source is
// equivalent to terminal input (design D7).
const ReplyPrefix = "[gofer web 回复] "

// OffCommand typed as the web reply switches relay off and lets the agent
// stop normally (the human is back at the keyboard).
const OffCommand = "/off"

const (
	defaultWait       = 540 * time.Second
	defaultPollSec    = 25
	defaultMaxMessage = 4000
	// transientRetries is how many consecutive transport failures the Stop wait
	// loop tolerates before giving up (never blocking the terminal on a dead hub).
	transientRetries = 5
	transientBackoff = 2 * time.Second
)

// noMessageFallback is relayed when the last assistant text cannot be read.
const noMessageFallback = "(无法读取最后一条消息，请查看终端)"

// Run executes one hook event. Network failures never surface as errors — the
// terminal must never be blocked by a dead hub — they are logged and the hook
// exits quietly. The returned error is reserved for programmer/IO faults the
// caller may report on stderr (exit 1, non-blocking for the CLI).
func Run(api API, p Payload, opts Options) (Result, error) {
	opts = opts.withDefaults()
	log := func(format string, args ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, "%s %s %s %s "+format+"\n",
				append([]any{opts.now().Format(time.RFC3339), p.Agent, shortID(p.SessionID), p.Event}, args...)...)
		}
	}
	r := &runner{api: api, p: p, opts: opts, log: log}
	switch p.Event {
	case "SessionStart":
		return Result{}, r.sessionStart()
	case "UserPromptSubmit":
		// Only a prompt a HUMAN typed means "back at the keyboard" (auto-off).
		// Claude Code also raises UserPromptSubmit for harness-generated turns:
		// our own Stop-hook continuation (ReplyPrefix), background-task
		// notifications, system reminders... Those are reported as injected so
		// the server keeps relay on.
		if IsHarnessPrompt(p.Prompt) {
			log("harness prompt %q → injected", head(p.Prompt, 60))
			r.beatAndLog(client.SessionHeartbeat{Event: p.Event, Injected: true})
			return Result{}, nil
		}
		log("human prompt %q", head(p.Prompt, 60))
		r.beatAndLog(client.SessionHeartbeat{Event: p.Event, Title: makeTitle(p.Cwd, p.Prompt)})
		return Result{}, nil
	case "Stop":
		return r.stop(), nil
	case "SessionEnd", "Interrupt":
		r.beatAndLog(client.SessionHeartbeat{Event: p.Event})
		return Result{}, nil
	case "Notification":
		hb := client.SessionHeartbeat{Event: p.Event, LastMessage: strings.TrimSpace(p.Message)}
		if p.NotificationType == "idle_prompt" {
			hb.State = "idle"
		}
		r.beatAndLog(hb)
		return Result{}, nil
	default:
		log("ignored")
		return Result{}, nil
	}
}

func (o Options) withDefaults() Options {
	if o.Wait <= 0 {
		o.Wait = defaultWait
	}
	if o.PollSec <= 0 {
		o.PollSec = defaultPollSec
	}
	if o.MaxMessage <= 0 {
		o.MaxMessage = defaultMaxMessage
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.sleep == nil {
		o.sleep = time.Sleep
	}
	return o
}

type runner struct {
	api  API
	p    Payload
	opts Options
	log  func(string, ...any)
}

func (r *runner) register(event string) (client.AgentSession, error) {
	a, err := r.api.RegisterSession(client.SessionRegister{
		SessionID: r.p.SessionID, Agent: r.p.Agent, ProjectKey: r.opts.ProjectKey,
		Runner: r.opts.Runner, Cwd: r.p.Cwd, Transcript: r.p.TranscriptPath,
		TmuxPane: r.opts.TmuxPane, Event: event,
	})
	if err != nil {
		r.log("register failed: %v", err)
	}
	return a, err
}

func (r *runner) sessionStart() error {
	if _, err := r.register(r.p.Event); err != nil {
		return nil // logged; the next event re-registers
	}
	if r.opts.CurrentFile != "" {
		if err := os.MkdirAll(filepath.Dir(r.opts.CurrentFile), 0o755); err == nil {
			_ = os.WriteFile(r.opts.CurrentFile, []byte(r.p.SessionID+"\n"), 0o644)
		}
	}
	r.log("registered")
	return nil
}

// heartbeat reports an event; an unknown session (hooks installed mid-session,
// or hub restarted with a fresh db) is registered first and the beat retried.
// ok is false when the hub could not be reached.
func (r *runner) heartbeat(hb client.SessionHeartbeat) (client.AgentSession, bool) {
	a, err := r.api.HeartbeatSession(r.p.SessionID, hb)
	if err != nil && client.StatusOf(err) == 404 {
		if _, rerr := r.register(hb.Event); rerr == nil {
			a, err = r.api.HeartbeatSession(r.p.SessionID, hb)
		}
	}
	if err != nil {
		r.log("heartbeat failed: %v", err)
		return client.AgentSession{}, false
	}
	return a, true
}

// beatAndLog is heartbeat plus a one-line trace of the resulting state.
func (r *runner) beatAndLog(hb client.SessionHeartbeat) {
	if a, ok := r.heartbeat(hb); ok {
		r.log("state=%s relay=%v", a.State, a.Relay)
	}
}

// stop is the relay main path (design §7).
func (r *runner) stop() Result {
	last := r.lastMessage()
	a, ok := r.heartbeat(client.SessionHeartbeat{Event: r.p.Event, LastMessage: last})
	if !ok {
		return Result{}
	}
	if !a.Relay {
		r.log("relay off, released")
		return Result{}
	}
	turn, err := r.api.OpenSessionTurn(r.p.SessionID, last, int64(r.opts.Wait/time.Second))
	if err != nil {
		r.log("open turn failed: %v", err) // 409 = relay flipped off in between
		return Result{}
	}
	r.log("turn %s open, waiting up to %s", turn.ID, r.opts.Wait)
	deadline := r.opts.now().Add(r.opts.Wait)
	failures := 0
	for {
		remaining := time.Until(deadline)
		if r.opts.now().After(deadline) || remaining <= 0 {
			break
		}
		wait := r.opts.PollSec
		if rs := int(remaining / time.Second); rs < wait {
			wait = rs
		}
		if wait < 1 {
			wait = 1
		}
		st, err := r.api.WaitSessionTurn(r.p.SessionID, turn.ID, wait)
		if err != nil {
			failures++
			r.log("wait failed (%d/%d): %v", failures, transientRetries, err)
			if failures >= transientRetries || client.StatusOf(err) == 404 {
				return Result{}
			}
			r.opts.sleep(transientBackoff)
			continue
		}
		failures = 0
		switch st.Outcome {
		case "answered":
			reply := strings.TrimSpace(st.Decision.Answer)
			if strings.EqualFold(reply, OffCommand) {
				if _, err := r.api.SetSessionRelay(r.p.SessionID, false); err != nil {
					r.log("relay off failed: %v", err)
				}
				r.log("answered /off, released")
				return Result{}
			}
			r.log("answered (%d bytes), continuing", len(reply))
			return Result{Blocked: true, Reason: ReplyPrefix + reply}
		case "expired", "relay_off":
			r.log("turn %s, released", st.Outcome)
			return Result{}
		}
	}
	r.log("wait budget exhausted, released")
	_, _ = r.api.HeartbeatSession(r.p.SessionID, client.SessionHeartbeat{Event: r.p.Event, State: "idle"})
	return Result{}
}

// lastMessage picks the agent's last assistant text: Codex hands it over on
// stdin, Claude keeps it in the transcript.
func (r *runner) lastMessage() string {
	if msg := strings.TrimSpace(r.p.LastAssistantMessage); msg != "" {
		return truncate(msg, r.opts.MaxMessage)
	}
	if r.p.TranscriptPath != "" {
		text, err := LastAssistantText(r.p.TranscriptPath, r.opts.MaxMessage)
		if err != nil {
			r.log("transcript read failed: %v", err)
		} else if text != "" {
			return text
		}
	}
	return noMessageFallback
}

// IsHarnessPrompt reports whether a UserPromptSubmit prompt was generated by
// the agent harness rather than typed by a human: empty, our own injected
// reply, or tagged system content (`<task-notification>`, `<system-reminder>`,
// anything starting with an XML-ish tag).
func IsHarnessPrompt(prompt string) bool {
	t := strings.TrimSpace(prompt)
	switch {
	case t == "":
		return true
	case strings.HasPrefix(t, strings.TrimSpace(ReplyPrefix)):
		return true
	case len(t) > 1 && t[0] == '<' && (t[1] == '/' || (t[1] >= 'a' && t[1] <= 'z') || (t[1] >= 'A' && t[1] <= 'Z')):
		return true // XML-ish tag such as <task-notification> / <system-reminder>
	case strings.Contains(t, "<task-notification>"), strings.Contains(t, "<system-reminder>"):
		return true
	}
	return false
}

// head returns the first n runes of s on one line (log helper).
func head(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}

// makeTitle names a session from its cwd and the first prompt:
// "<cwd base>: <first line of prompt, ≤40 runes>".
func makeTitle(cwd, prompt string) string {
	base := filepath.Base(strings.TrimRight(cwd, "/\\"))
	if base == "." || base == "" || base == string(filepath.Separator) {
		base = cwd
	}
	line := strings.TrimSpace(prompt)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if utf8.RuneCountInString(line) > 40 {
		rs := []rune(line)
		line = string(rs[:40]) + "…"
	}
	if line == "" {
		return base
	}
	return base + ": " + line
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// BlockJSON renders the Stop-hook continuation both Claude Code and Codex
// understand (legacy `decision` form): the reason becomes the next prompt.
func BlockJSON(reason string) []byte {
	b, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	return b
}
