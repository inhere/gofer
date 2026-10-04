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
	"time"

	"github.com/inhere/gofer/internal/config"
)

// Manager keeps one stream-json Claude process per local runner. A process is
// never shared with a remote worker; remote paths use the worker's own Manager.
type Manager struct {
	mu        sync.Mutex
	command   string
	idle      time.Duration
	processes map[string]*process
}

// New creates a resident messenger manager. Non-positive idle values use the
// shipped ten minute default, matching the pre-package implementation.
func New(command string, idle time.Duration) *Manager {
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	return &Manager{command: strings.TrimSpace(command), idle: idle, processes: make(map[string]*process)}
}

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

// Status reports the process state for a runner.
func (m *Manager) Status(runner string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.processes[runner]
	if p == nil {
		return "stopped"
	}
	select {
	case <-p.done:
		return "stopped"
	default:
		return "running"
	}
}

// ResidentMessengerStatus preserves the session-relay assembly seam while the
// implementation lives in this package.
func (m *Manager) ResidentMessengerStatus(runner string) string { return m.Status(runner) }

// Send writes one stream-json request and waits for its result.
func (m *Manager) Send(ctx context.Context, runner, cwd string, command []string) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(runner), config.BuiltinLocalRunner) {
		return "", errors.New("resident messenger only supports the local runner")
	}
	prompt := promptFromCommand(command)
	payload, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": prompt},
	})
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		p, err := m.process(runner, cwd, command)
		if err != nil {
			return "", err
		}
		p.mu.Lock()
		if p.isStopped() {
			p.mu.Unlock()
			m.remove(runner, p)
			p.stop()
			if attempt == 0 {
				continue
			}
			return "", errMessengerProcessClosed
		}
		_, writeErr := p.stdin.Write(append(payload, '\n'))
		if writeErr != nil {
			p.mu.Unlock()
			m.remove(runner, p)
			p.stop()
			if attempt == 0 && retryableWriteError(writeErr) {
				continue
			}
			return "", fmt.Errorf("resident messenger write: %w", writeErr)
		}
		select {
		case event := <-p.events:
			p.mu.Unlock()
			if event.err != nil {
				m.remove(runner, p)
				p.stop()
				return "", event.err
			}
			p.touch()
			return event.output, nil
		case err := <-p.done:
			p.mu.Unlock()
			m.remove(runner, p)
			return "", fmt.Errorf("resident messenger exited: %w", err)
		case <-ctx.Done():
			p.mu.Unlock()
			m.remove(runner, p)
			p.stop()
			return "", ctx.Err()
		}
	}
	return "", errMessengerProcessClosed
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
		select {
		case <-p.done:
			delete(m.processes, runner)
		case <-p.stopped:
			delete(m.processes, runner)
		default:
			return p, nil
		}
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return nil, errors.New("resident messenger command is empty")
	}
	cmd := exec.Command(command[0], "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--allowedTools", "SendMessage,ListAgents")
	// The cwd comes from the target session's last report and may be gone (a
	// removed worktree); spawning there fails with "chdir: no such file". The
	// messenger only needs SendMessage/ListAgents, so any existing dir works.
	cmd.Dir = usableDir(cwd, homeDir())
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
	p := &process{cmd: cmd, stdin: stdin, events: make(chan event, 1), done: make(chan error, 1), stopped: make(chan struct{}), idle: m.idle}
	go p.read(stdout)
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	p.touch()
	m.processes[runner] = p
	return p, nil
}

func (p *process) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var frame struct {
			Type   string          `json:"type"`
			Result json.RawMessage `json:"result"`
			IsErr  bool            `json:"is_error"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Type != "result" {
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
			p.events <- event{output: output}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("stream closed")
	}
	p.done <- err
}

func (p *process) touch() {
	if p.idle <= 0 {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(p.idle, p.stop)
}

func (p *process) stop() {
	p.killOnce.Do(func() {
		close(p.stopped)
		if p.timer != nil {
			p.timer.Stop()
		}
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
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	events   chan event
	done     chan error
	stopped  chan struct{}
	idle     time.Duration
	timer    *time.Timer
	killOnce sync.Once
}

type event struct {
	output string
	err    error
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

func homeDir() string {
	dir, _ := os.UserHomeDir()
	return dir
}
