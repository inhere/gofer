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
)

// Manager keeps one stream-json Claude process per local runner. A process is
// never shared with a remote worker; remote paths use the worker's own Manager.
type Manager struct {
	mu        sync.Mutex
	command   string
	idle      time.Duration
	processes map[string]*process
	stats     map[string]*runnerStats
}

// New creates a resident messenger manager. Non-positive idle values use the
// shipped ten minute default, matching the pre-package implementation.
func New(command string, idle time.Duration) *Manager {
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	return &Manager{command: strings.TrimSpace(command), idle: idle,
		processes: make(map[string]*process), stats: make(map[string]*runnerStats)}
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
		p, err := m.process(runner, cwd, command)
		if err != nil {
			return event{}, err
		}
		p.mu.Lock()
		if p.isStopped() {
			p.mu.Unlock()
			m.remove(runner, p)
			p.stop()
			if attempt == 0 {
				continue
			}
			return event{}, errMessengerProcessClosed
		}
		_, writeErr := p.stdin.Write(append(payload, '\n'))
		if writeErr != nil {
			p.mu.Unlock()
			m.remove(runner, p)
			p.stop()
			if attempt == 0 && retryableWriteError(writeErr) {
				continue
			}
			return event{}, fmt.Errorf("resident messenger write: %w", writeErr)
		}
		select {
		case ev := <-p.events:
			p.mu.Unlock()
			if ev.err != nil {
				m.remove(runner, p)
				p.stop()
				return event{}, ev.err
			}
			p.touch()
			return ev, nil
		case err := <-p.done:
			p.mu.Unlock()
			m.remove(runner, p)
			return event{}, fmt.Errorf("resident messenger exited: %w", err)
		case <-ctx.Done():
			p.mu.Unlock()
			m.remove(runner, p)
			p.stop()
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
	cmd := exec.Command(command[0], "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--allowedTools", "SendMessage,ListAgents")
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
		stopped: make(chan struct{}), idle: m.idle}
	st := m.statsFor(runner)
	st.startedAt = time.Now().Unix()
	_, _ = fmt.Fprintf(st.stderr, "[gofer] resident messenger started (pid %d, dir %q)\n", cmd.Process.Pid, cmd.Dir)
	go p.read(stdout)
	go func() { _, _ = io.Copy(st.stderr, stderr) }()
	p.touch()
	m.processes[runner] = p
	return p, nil
}

func (p *process) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	toolNames := map[string]string{}
	listing := ""
	for scanner.Scan() {
		var frame struct {
			Type    string          `json:"type"`
			Result  json.RawMessage `json:"result"`
			IsErr   bool            `json:"is_error"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			ToolUseResult json.RawMessage `json:"tool_use_result"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		switch frame.Type {
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
		default:
			continue
		}
		output := strings.TrimSpace(string(frame.Result))
		var text string
		if json.Unmarshal(frame.Result, &text) == nil {
			output = text
		}
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
	p.timer = time.AfterFunc(p.idle, p.stop)
	p.timerMu.Unlock()
	p.deadline.Store(time.Now().Add(p.idle).Unix())
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

func promptFromCommand(command []string) string {
	for i, part := range command {
		if part == "-p" && i+1 < len(command) {
			return command[i+1]
		}
	}
	return ""
}

func scrubClaudeEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
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
