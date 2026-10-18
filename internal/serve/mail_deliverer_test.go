package serve

import (
	"os"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// serve must start the deliverer and re-apply the mail block on reload (a source guard, like
// TestServeInjectsCoreMailbox: a whole serve process is not worth assembling in a unit test).
func TestServeStartsAndReloadsMailDeliverer(t *testing.T) {
	b, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"startMailDeliverer(cr, srv, cfg)", "defer dl.Stop()", "applyMailSettings(cr.Mail, mailDeliverer, next)"} {
		if !strings.Contains(src, want) {
			t.Fatalf("serve.go no longer contains %q", want)
		}
	}
}

func TestIsSugaAgent(t *testing.T) {
	reg := agent.NewRegistry(&config.Config{Agents: map[string]config.AgentConfig{
		"suga":       {Type: agent.TypeCLIAgent, Command: "suga"},
		"my-suga":    {Type: agent.TypeCLIAgent, Command: "/opt/bin/suga.exe"},
		"claude":     {Type: agent.TypeCLIAgent, Command: "claude"},
		"suga-proxy": {Type: agent.TypeExec, Command: "suga"},
	}})
	for key, want := range map[string]bool{
		"suga": true, " suga ": true, "my-suga": true, "claude": false, "suga-proxy": false, "unknown": false, "": false,
	} {
		if got := isSugaAgent(reg, key); got != want {
			t.Errorf("isSugaAgent(%q) = %v, want %v", key, got, want)
		}
	}
	if !isSugaAgent(nil, "suga") || isSugaAgent(nil, "claude") {
		t.Error("with no registry only the built-in key counts")
	}
}
