package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/logx"
)

// captureStderr swaps os.Stderr for a pipe for the duration of fn and returns
// what was written. logx handlers hold the *os.File they were created with, so
// the swap must happen before configureForwardLogging installs them.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stderr = orig; w.Close() }()
		fn()
	}()
	return <-done
}

// resetLogx puts the process logger back to the plain stderr handler after a
// test that configured a file sink or silenced stderr.
func resetLogx(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { logx.Setup(); slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })
}

func TestTunnelForwardLogFlagsExclusive(t *testing.T) {
	saved := tunnelOpts
	t.Cleanup(func() { tunnelOpts = saved })
	tunnelOpts.logFile, tunnelOpts.logDir, tunnelOpts.worker = "a.log", "logs", "w1"
	err := runTunnelForward(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want mutually exclusive error, got %v", err)
	}
}

func TestTunnelForwardLogDirUniqueNames(t *testing.T) {
	now := time.Date(2026, 9, 15, 10, 30, 0, 0, time.Local)
	a, explicitA := forwardLogPath("", "logs", now, 100)
	b, _ := forwardLogPath("", "logs", now, 101)
	c, _ := forwardLogPath("", "logs", now.Add(time.Second), 100)
	if !explicitA {
		t.Fatal("--log-dir is an explicit location")
	}
	if a == b || a == c {
		t.Fatalf("log names must differ per pid and start time: %s %s %s", a, b, c)
	}
	if filepath.Base(a) != "forward-20260915-103000-100.log" {
		t.Fatalf("name %s", filepath.Base(a))
	}
	if p, explicit := forwardLogPath("x.log", "", now, 1); p != "x.log" || !explicit {
		t.Fatalf("--log-file must win verbatim: %s %v", p, explicit)
	}
	t.Setenv("GOFER_CONFIG_DIR", t.TempDir())
	d, explicit := forwardLogPath("", "", now, 7)
	if explicit || !strings.HasSuffix(filepath.ToSlash(d), "/run/tunnels/forward-20260915-103000-7.log") {
		t.Fatalf("default path %s explicit=%v", d, explicit)
	}
}

func TestTunnelForwardQuietStillWritesFile(t *testing.T) {
	resetLogx(t)
	logFile := filepath.Join(t.TempDir(), "fwd.log")
	out := captureStderr(t, func() {
		logx.Setup()
		if err := configureForwardLogging(true, logFile, "", time.Now(), 1); err != nil {
			t.Fatal(err)
		}
		forwardEventSink("w1")("forward.started", "local", "127.0.0.1:1", "target", "dev:2", "network", "tcp")
		forwardEventSink("w1")("session.closed", "session_id", "s1", "tunnel_id", "t1", "close_reason", "client_closed", "bytes_up", int64(4), "bytes_down", int64(7))
	})
	if out != "" {
		t.Fatalf("--quiet must leave the terminal silent, got %q", out)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 JSONL lines, got %d: %s", len(lines), b)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("not JSON: %v: %s", err, lines[0])
	}
	if row["event"] != "forward.started" || row["component"] != "forward" || row["worker"] != "w1" || row["operation_id"] == "" {
		t.Fatalf("forward.started row: %v", row)
	}
	if err := json.Unmarshal([]byte(lines[1]), &row); err != nil {
		t.Fatal(err)
	}
	if row["event"] != "session.closed" || row["tunnel_id"] != "t1" || row["close_reason"] != "client_closed" || row["bytes_down"] != float64(7) {
		t.Fatalf("session.closed row: %v", row)
	}
}

func TestTunnelForwardNotQuietPrintsEachEventOnce(t *testing.T) {
	resetLogx(t)
	logFile := filepath.Join(t.TempDir(), "fwd.log")
	out := captureStderr(t, func() {
		logx.Setup() // bind the stderr sink to the captured pipe, as main does at start
		if err := configureForwardLogging(false, logFile, "", time.Now(), 1); err != nil {
			t.Fatal(err)
		}
		forwardEventSink("w1")("forward.started", "local", "127.0.0.1:1", "target", "dev:2", "network", "tcp")
	})
	// msg and event both name the event, so count lines, not substrings.
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "event=forward.started") {
		t.Fatalf("terminal should show the event on exactly one line, got %q", out)
	}
	if b, _ := os.ReadFile(logFile); !bytes.Contains(b, []byte(`"event":"forward.started"`)) {
		t.Fatalf("file missing the event: %s", b)
	}
}

func TestTunnelForwardDefaultLogPathFallback(t *testing.T) {
	resetLogx(t)
	// GOFER_CONFIG_DIR pointing at a regular file makes <dir>/run/tunnels
	// uncreatable, which is the "config dir not writable" case.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFER_CONFIG_DIR", blocker)
	out := captureStderr(t, func() {
		logx.Setup()
		if err := configureForwardLogging(false, "", "", time.Now(), 1); err != nil {
			t.Fatalf("implicit default path must degrade, not fail: %v", err)
		}
	})
	if !strings.Contains(out, "warning") {
		t.Fatalf("expected a stderr warning about the log file, got %q", out)
	}
	// The same location given explicitly is fatal.
	explicit := filepath.Join(blocker, "fwd.log")
	captureStderr(t, func() {
		if err := configureForwardLogging(false, explicit, "", time.Now(), 1); err == nil {
			t.Fatal("explicit --log-file under an unwritable path must fail")
		}
	})
}

func TestTunnelCmdHelpShowsLogFlags(t *testing.T) {
	var fwd string
	for _, s := range NewTunnelCmd().Subs {
		if s.Name != "forward" {
			continue
		}
		var sb strings.Builder
		s.Config(s)
		for _, o := range s.Opts() {
			sb.WriteString(o.Name + " " + o.Desc + "\n")
		}
		fwd = sb.String()
	}
	for _, flag := range []string{"log-file", "log-dir", "quiet"} {
		if !strings.Contains(fwd, flag) {
			t.Fatalf("forward options missing %s: %s", flag, fwd)
		}
	}
}
