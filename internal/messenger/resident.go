// Package messenger owns the stream-json process bridge used to deliver a
// message to an agent session. It is intentionally independent of HTTP and
// worker entry points so both server-local and worker dispatch paths share the
// same process lifecycle and environment rules.
package messenger

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/util"
)

// Manager keeps one stream-json Claude process per local runner. A process is
// never shared with a remote worker; remote paths use the worker's own Manager.
type Manager struct {
	mu          sync.Mutex
	command     string
	idle        time.Duration
	maxMessages int           // rotate the resident process after this many messages (0 = unlimited)
	maxAge      time.Duration // rotate it after this long (0 = unlimited)
	processes   map[string]*process
	stats       map[string]*runnerStats

	// replies is what a resident process learns when another Claude session
	// messages IT (a target answering a web message with SendMessage): see reply.go.
	replies replyState
}

// New creates a resident messenger manager. Non-positive idle values use the
// shipped ten minute default, matching the pre-package implementation. The
// rotation limits start at the shipped defaults (config.DefaultMessengerMaxMessages /
// DefaultMessengerMaxAgeSec); SetRotation overrides them from the config.
func New(command string, idle time.Duration) *Manager {
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	return &Manager{command: strings.TrimSpace(command), idle: idle,
		maxMessages: config.DefaultMessengerMaxMessages, maxAge: time.Duration(config.DefaultMessengerMaxAgeSec) * time.Second,
		processes: make(map[string]*process), stats: make(map[string]*runnerStats)}
}

// SetRotation sets the rotation limits: a resident process that has handled
// maxMessages messages or lived maxAge is replaced BEFORE the next message, never
// mid-delivery. Non-positive values switch that limit off (no rotation).
func (m *Manager) SetRotation(maxMessages int, maxAge time.Duration) {
	if maxMessages < 0 {
		maxMessages = 0
	}
	if maxAge < 0 {
		maxAge = 0
	}
	m.mu.Lock()
	m.maxMessages, m.maxAge = maxMessages, maxAge
	m.mu.Unlock()
}

// Command is the configured messenger binary ("" on a worker, where each
// dispatch carries its own command).
func (m *Manager) Command() string { return m.command }

// SetIdle updates the idle lifetime used by newly created resident processes.
// Existing processes keep their current timer until the next request refreshes it.
func (m *Manager) SetIdle(idle time.Duration) {
	if idle <= 0 {
		return
	}
	m.mu.Lock()
	m.idle = idle
	m.mu.Unlock()
}

// Status reports the messenger state for a runner: stopped (no process), idle
// (process alive, nothing in flight) or busy (a request is queued or running).
func (m *Manager) Status(runner string) string { return m.Snapshot(runner).Status }

// ResidentMessengerStatus preserves the session-relay assembly seam while the
// implementation lives in this package.
func (m *Manager) ResidentMessengerStatus(runner string) string { return m.Status(runner) }

// Send writes one stream-json request and waits for its result. target is the
// addressed session's name; it only labels the delivery history.
func (m *Manager) Send(ctx context.Context, runner, cwd, target string, command []string) (string, error) {
	// G043: "server" is the built-in runner's other spelling; state is keyed by the
	// canonical key either way.
	runner = config.NormalizeRunnerName(strings.ToLower(strings.TrimSpace(runner)))
	if runner != config.BuiltinLocalRunner {
		return "", errors.New("resident messenger only supports the local runner")
	}
	prompt := promptFromCommand(command)
	done := m.begin(runner, "send", target, originalMessage(prompt))
	ev, err := m.roundTrip(ctx, runner, cwd, command, prompt)
	done(err)
	return ev.output, err
}

// roundTrip writes one user prompt to the runner's resident process and waits
// for its result frame, restarting a dead process once.
func (m *Manager) roundTrip(ctx context.Context, runner, cwd string, command []string, prompt string) (event, error) {
	payload, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": prompt},
	})
	if err != nil {
		return event{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		p, err := m.processForRequest(runner, cwd, command)
		if err != nil {
			return event{}, err
		}
		p.mu.Lock()
		// The idle timer must not fire while a request is in flight: a reply slower
		// than the idle window used to get the process killed mid-request
		// (gofer-r7am C3). pause fails when the timer already fired, so a process
		// that is idling out right now is replaced instead of written to.
		if !p.pause() {
			p.mu.Unlock()
			m.remove(runner, p)
			p.stopWithReason(ReasonIdle)
			if attempt == 0 {
				continue
			}
			return event{}, errMessengerProcessClosed
		}
		// Only a result that answers THIS request may reach p.events; the reader
		// drops any result that arrives while nobody waits (see read).
		p.waiting.Store(true)
		_, writeErr := p.stdin.Write(append(payload, '\n'))
		if writeErr != nil {
			p.waiting.Store(false)
			p.mu.Unlock()
			m.remove(runner, p)
			p.stopWithReason(ReasonFailed)
			if attempt == 0 && retryableWriteError(writeErr) {
				continue
			}
			return event{}, fmt.Errorf("resident messenger write: %w", writeErr)
		}
		select {
		case ev := <-p.events:
			p.waiting.Store(false)
			p.mu.Unlock()
			if ev.err != nil {
				m.remove(runner, p)
				p.stopWithReason(ReasonFailed)
				return event{}, ev.err
			}
			p.touch()
			return ev, nil
		case err := <-p.done:
			p.waiting.Store(false)
			p.mu.Unlock()
			m.remove(runner, p)
			p.stopWithReason(ReasonFailed)
			return event{}, fmt.Errorf("resident messenger exited: %w", err)
		case <-ctx.Done():
			p.waiting.Store(false)
			p.mu.Unlock()
			m.remove(runner, p)
			p.stopWithReason(ReasonFailed)
			return event{}, ctx.Err()
		}
	}
	return event{}, errMessengerProcessClosed
}

var errMessengerProcessClosed = errors.New("resident messenger process closed")

func retryableWriteError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "broken pipe") || strings.Contains(text, "file already closed") || strings.Contains(text, "pipe closed")
}

func (m *Manager) process(runner, cwd string, command []string) (*process, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.processes[runner]; p != nil {
		if p.gone() {
			delete(m.processes, runner)
		} else {
			return p, nil
		}
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return nil, errors.New("resident messenger command is empty")
	}
	// --name: the receiving session sees messages from "gofer-web" (the person on the
	// web), not from an auto-named session of the target's directory (gofer-02fu).
	cmd := exec.Command(command[0], "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--allowedTools", "SendMessage,ListAgents", "--name", "gofer-web")
	procattr.Background(cmd)
	// The cwd comes from the target session's last report and may be gone (a
	// removed worktree); spawning there fails with "chdir: no such file". The
	// messenger only needs SendMessage/ListAgents, so any existing dir works:
	// the session's cwd, then this machine's default workspace, then the home.
	cmd.Dir = usableDir(cwd, workspaceDir(), homeDir())
	cmd.Env = scrubClaudeEnv(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("resident messenger stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("resident messenger stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("resident messenger stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("resident messenger start: %w", err)
	}
	p := &process{cmd: cmd, stdin: stdin, events: make(chan event, 1), done: make(chan error, 1), exited: make(chan struct{}),
		stopped: make(chan struct{}), idle: m.idle, owner: m, runner: runner, createdAt: time.Now()}
	st := m.statsFor(runner)
	st.startedAt = time.Now().Unix()
	_, _ = fmt.Fprintf(st.stderr, "[gofer] resident messenger started (pid %d, dir %q)\n", cmd.Process.Pid, cmd.Dir)
	go p.read(stdout)
	go func() { _, _ = io.Copy(st.stderr, stderr) }()
	// No idle timer yet: the caller writes its request right away, and the timer
	// starts once that request is answered (roundTrip's touch).
	m.processes[runner] = p
	return p, nil
}

func (p *process) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	toolNames := map[string]string{}
	listing := ""
	// peerTurn is true between command_lifecycle started/completed: Claude Code runs
	// a message another session sent to this process (a target answering a web
	// message) as its own queued command, bracketed by those frames, while turns
	// started by a stdin request never are (verified 2026-10-10, Claude Code
	// 2.1.296, including a request queued behind a running peer turn).
	peerTurn := false
	for scanner.Scan() {
		var frame struct {
			Type      string          `json:"type"`
			Subtype   string          `json:"subtype"`
			State     string          `json:"state"`
			SessionID string          `json:"session_id"`
			Cwd       string          `json:"cwd"`
			Result    json.RawMessage `json:"result"`
			IsErr     bool            `json:"is_error"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"tool_use_result"`
			Usage         struct {
				InputTokens         int64 `json:"input_tokens"`
				OutputTokens        int64 `json:"output_tokens"`
				CacheReadTokens     int64 `json:"cache_read_input_tokens"`
				CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			} `json:"usage"`
			TotalCostUSD float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		switch frame.Type {
		case "system":
			if frame.Subtype == "init" {
				p.setSession(frame.SessionID, frame.Cwd)
			}
			continue
		case "command_lifecycle":
			switch frame.State {
			case "started":
				peerTurn = true
			case "completed":
				peerTurn = false
			}
			continue
		case "assistant":
			for _, b := range contentBlocks(frame.Message.Content) {
				if b.Type == "tool_use" && b.ID != "" {
					toolNames[b.ID] = b.Name
				}
			}
			continue
		case "user":
			// The ListAgents tool_result is the reliable listing: the final result
			// frame is only the model's retelling of it.
			for _, b := range contentBlocks(frame.Message.Content) {
				if b.Type == "tool_result" && toolNames[b.ToolUseID] == "ListAgents" {
					if text := blockText(b.Content); text != "" {
						listing = text
					}
				}
			}
			var tur struct {
				Listing string `json:"listing"`
			}
			if json.Unmarshal(frame.ToolUseResult, &tur) == nil && strings.TrimSpace(tur.Listing) != "" {
				listing = tur.Listing
			}
			continue
		case "result":
			// Every result frame is one turn this process now carries in its context,
			// whether we asked for it or a peer did, so it counts toward the rotation
			// limit; the same frame carries the token / cost accounting.
			p.messages.Add(1)
			p.recordUsage(frame.Usage.InputTokens, frame.Usage.OutputTokens, frame.Usage.CacheReadTokens,
				frame.Usage.CacheCreationTokens, frame.TotalCostUSD)
		default:
			continue
		}
		output := strings.TrimSpace(string(frame.Result))
		var text string
		if json.Unmarshal(frame.Result, &text) == nil {
			output = text
		}
		// An unsolicited turn (a peer's message, or anything answered while no
		// request waits) must never reach p.events: it used to be read as the NEXT
		// request's result, so every later delivery reported the previous reply's
		// retelling and a second one blocked this reader for good (gofer-6er0).
		if peerTurn || !p.waiting.Load() {
			listing = ""
			p.unsolicited(output)
			continue
		}
		p.waiting.Store(false) // one result per request; a stray second one is unsolicited
		p.scanReplies("")      // a reply absorbed into this turn is in the transcript too
		if frame.IsErr {
			p.events <- event{err: errors.New(output)}
		} else {
			p.events <- event{output: output, listing: listing}
		}
		listing = ""
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("stream closed")
	}
	p.done <- err
	close(p.exited)
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func contentBlocks(raw json.RawMessage) []contentBlock {
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// blockText flattens a tool_result content (a string, or a list of text blocks).
func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		out = append(out, p.Text)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (p *process) touch() {
	if p.idle <= 0 {
		return
	}
	p.timerMu.Lock()
	if p.timer != nil {
		p.timer.Stop()
	}
	// The idle timer is one of the recorded rebuild reasons (ReasonIdle).
	p.timer = time.AfterFunc(p.idle, func() { p.stopWithReason(ReasonIdle) })
	p.timerMu.Unlock()
	p.deadline.Store(time.Now().Add(p.idle).Unix())
}

// pause stops the idle timer for a request about to be written. It reports false
// when the process must not be used: the timer already fired (stop is running or
// about to) or the process was stopped. The caller holds p.mu, so no other request
// can re-arm the timer before this one's touch.
func (p *process) pause() bool {
	p.timerMu.Lock()
	defer p.timerMu.Unlock()
	if p.timer != nil {
		fired := !p.timer.Stop()
		p.timer = nil
		if fired {
			return false
		}
	}
	return !p.isStopped()
}

func (p *process) stop() {
	p.killOnce.Do(func() {
		close(p.stopped)
		p.timerMu.Lock()
		if p.timer != nil {
			p.timer.Stop()
		}
		p.timerMu.Unlock()
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
}

func (p *process) isStopped() bool {
	select {
	case <-p.stopped:
		return true
	default:
		return false
	}
}

// gone reports whether the process was stopped or its stdout closed.
func (p *process) gone() bool {
	if p.isStopped() {
		return true
	}
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (m *Manager) remove(runner string, p *process) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processes[runner] == p {
		delete(m.processes, runner)
	}
}

// processForRequest returns the process to write the next request to. A process
// that reached a rotation limit is retired first: the message that tripped the
// limit was already answered, so the rebuild lands between two messages instead
// of cutting one off (gofer-li3t).
func (m *Manager) processForRequest(runner, cwd string, command []string) (*process, error) {
	p, err := m.process(runner, cwd, command)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	maxMessages, maxAge := m.maxMessages, m.maxAge
	m.mu.Unlock()
	if reason := p.rotationReason(maxMessages, maxAge); reason != "" {
		m.remove(runner, p)
		m.retire(p, reason)
		return m.process(runner, cwd, command)
	}
	return p, nil
}

// retire stops p and records why it was rebuilt, together with what it handled
// (messages, lifetime, token / cost usage). The first reason wins: a process that
// idled out and is then noticed by a request is recorded once.
func (m *Manager) retire(p *process, reason string) {
	p.reasonOnce.Do(func() {
		now := time.Now()
		r := Rotation{At: now.Unix(), Reason: reason, Messages: p.messages.Load(), AgeSec: int64(p.age(now) / time.Second)}
		if u := p.usageSnapshot(); !u.empty() {
			r.Usage = &u
		}
		m.mu.Lock()
		m.statsFor(p.runner).recordRotation(r)
		m.mu.Unlock()
	})
	p.stop()
}

// stopWithReason stops the process and records the rebuild reason. A test double
// built without a Manager only stops.
func (p *process) stopWithReason(reason string) {
	if p.owner == nil {
		p.stop()
		return
	}
	p.owner.retire(p, reason)
}

func promptFromCommand(command []string) string {
	for i, part := range command {
		if part == "-p" && i+1 < len(command) {
			return command[i+1]
		}
	}
	return ""
}

func scrubClaudeEnv(env []string) []string {
	out := make([]string, 0, util.CapSum(len(env), 1))
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(name), "CLAUDE") || name == "GOFER_MESSENGER" {
			continue
		}
		out = append(out, item)
	}
	// The messenger is launched from the target session's project directory. Its
	// own Claude hooks must see this marker and exit without registering a second
	// agent session or waiting on relay state.
	out = append(out, "GOFER_MESSENGER=1")
	return out
}

type process struct {
	mu      sync.Mutex
	owner   *Manager // nil for test doubles built without a Manager
	runner  string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	events  chan event
	done    chan error
	exited  chan struct{} // closed once the stdout reader ends
	stopped chan struct{}
	idle    time.Duration
	// timerMu guards timer: touch replaces it while the idle timer's own goroutine
	// may be running stop. Separate from mu, which a round trip can hold for long.
	timerMu  sync.Mutex
	timer    *time.Timer
	deadline atomic.Int64 // unix seconds the idle timer fires at
	killOnce sync.Once
	// waiting is true while a round trip waits for its result frame.
	waiting atomic.Bool
	// createdAt is when the child started (the rotation age limit) and messages
	// counts the turns it handled (result frames, incl. peer turns).
	createdAt time.Time
	messages  atomic.Int64
	// usageMu guards usage: the accounting the result frames reported.
	usageMu sync.Mutex
	usage   Usage
	// reasonOnce keeps the FIRST rebuild reason of this process, so an idle exit
	// and the request that notices it record one rotation, not two.
	reasonOnce sync.Once
	// sessMu guards the Claude session id / cwd of the latest system/init frame
	// (where the process's own transcript lives).
	sessMu    sync.Mutex
	sessionID string
	sessCwd   string
}

// age is how long the process has lived (0 for a test double without createdAt).
func (p *process) age(now time.Time) time.Duration {
	if p.createdAt.IsZero() {
		return 0
	}
	return now.Sub(p.createdAt)
}

func (p *process) recordUsage(in, out, cacheRead, cacheCreation int64, cost float64) {
	p.usageMu.Lock()
	p.usage.add(in, out, cacheRead, cacheCreation, cost)
	p.usageMu.Unlock()
}

func (p *process) usageSnapshot() Usage {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	return p.usage
}

// rotationReason reports why this process must be replaced before the next
// message is written to it ("" = it may serve another one).
func (p *process) rotationReason(maxMessages int, maxAge time.Duration) string {
	if maxMessages > 0 && p.messages.Load() >= int64(maxMessages) {
		return ReasonMaxMessages
	}
	if maxAge > 0 && p.age(time.Now()) >= maxAge {
		return ReasonMaxAge
	}
	return ""
}

type event struct {
	output  string
	listing string
	err     error
}

// usableDir returns the first candidate that is an existing directory, or ""
// (inherit this process's cwd) when none is.
func usableDir(candidates ...string) string {
	for _, dir := range candidates {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return ""
}

// workspaceDir is this machine's default workspace (GOFER_WORKSPACE or
// ~/.gofer/workspace); it may not exist, usableDir skips it then.
func workspaceDir() string {
	dir, _ := config.WorkspaceDir("")
	return dir
}

func homeDir() string {
	dir, _ := os.UserHomeDir()
	return dir
}

// Close stops every resident process and waits until its stdout reader ended.
// The manager stays usable: the next request starts a new process.
func (m *Manager) Close() {
	m.mu.Lock()
	procs := make([]*process, 0, len(m.processes))
	for runner, p := range m.processes {
		procs = append(procs, p)
		delete(m.processes, runner)
	}
	m.mu.Unlock()
	for _, p := range procs {
		p.stop()
		<-p.exited
	}
}
