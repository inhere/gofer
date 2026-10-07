//go:build windows

package servicemgr

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/daemon"
)

func TestTCPListenerOwnerIsBoundToPID(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	owned, err := ownsListeningPort(os.Getpid(), host, uint16(port))
	if err != nil || !owned {
		t.Fatalf("own listener not found: owned=%v err=%v", owned, err)
	}
	foreign, err := ownsListeningPort(os.Getppid(), host, uint16(port))
	if err != nil || foreign {
		t.Fatalf("foreign PID claimed listener: owned=%v err=%v", foreign, err)
	}
}

func TestForeignHealthResponseDoesNotCompleteUpgrade(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestForeignHealthChild$")
	cmd.Env = append(os.Environ(), "GOFER_T5_FOREIGN_HEALTH_ADDR="+addr)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("foreign health child not ready: %q %v", line, err)
	}
	m, spec := fixtureSpec(t)
	spec.Serve.Addr = addr
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	identity, err := daemon.CurrentProcessIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveState(State{SchemaVersion: StateSchema, Name: m.Name, Server: identity}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(spec.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WritePIDFile(filepath.Join(spec.RuntimeDir, "serve.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := waitManagedHealthy(context.Background(), m, spec, "", 450*time.Millisecond); err == nil {
		t.Fatal("foreign /health was mistaken for this PID's port")
	}
}

func TestForeignHealthChild(t *testing.T) {
	addr := os.Getenv("GOFER_T5_FOREIGN_HEALTH_ADDR")
	if addr == "" {
		return
	}
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})}
	go server.Serve(listener)
	_, _ = os.Stdout.WriteString("ready\n")
	time.Sleep(2 * time.Second)
	_ = server.Close()
}
