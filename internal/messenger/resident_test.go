package messenger

import (
	"context"
	"testing"
	"time"
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
