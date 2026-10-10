package messenger

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
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
	if got != "已发送" {
		t.Fatalf("resident output = %q, want 已发送", got)
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
	if got != "GOFER_MESSENGER=1" {
		t.Fatalf("resident child env = %q, want GOFER_MESSENGER=1", got)
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
	if got != "slow reply" {
		t.Fatalf("output = %q, want slow reply", got)
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
