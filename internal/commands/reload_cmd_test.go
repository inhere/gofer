package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"
)

func TestServeReloadCommandRegistered(t *testing.T) {
	cmd := NewServeCmd()
	if findSub(t, cmd, "reload") == nil {
		t.Fatal("serve reload must be registered")
	}
}

func TestWorkerReloadLocalFlagRegistered(t *testing.T) {
	cmd := NewWorkerReloadCmd()
	bound := &gcli.Command{Name: cmd.Name}
	cmd.Config(bound)
	if bound.Opt("local") == nil {
		t.Fatal("worker reload must bind --local")
	}
}
