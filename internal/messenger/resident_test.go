package messenger

import (
	"context"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func TestResidentMessengerPackageParity(t *testing.T) {
	m := New("claude", time.Minute)
	if got := m.Status("local"); got != "stopped" {
		t.Fatalf("new resident messenger status = %q, want stopped", got)
	}
	if _, err := m.Send(context.Background(), "remote", "", []string{"claude", "-p", "hello"}); err == nil {
		t.Fatal("remote runner should be rejected before starting a process")
	}
}

func TestResidentMessengerSendsStreamJSON(t *testing.T) {
	t.Setenv("GOFER_TEST_STREAM_JSON", "1")
	m := New("", time.Minute)
	command := append(testcmd.Cmd(t, "stream-json-fake"), "-p", "hello")
	got, err := m.Send(context.Background(), "local", "", command)
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
	got, err := m.Send(context.Background(), "local", "", command)
	if err != nil {
		t.Fatal(err)
	}
	if got != "GOFER_MESSENGER=1" {
		t.Fatalf("resident child env = %q, want GOFER_MESSENGER=1", got)
	}
}
