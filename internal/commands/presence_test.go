package commands

import (
	"testing"

	"github.com/gookit/gcli/v3"
)

// TestPresenceCmdSubsRegistered verifies the presence group registers list/send/
// inbox and carries the `driver` alias (the driver-agent surface, distinct from
// the job-agent `agent` command).
func TestPresenceCmdSubsRegistered(t *testing.T) {
	cmd := NewPresenceCmd()
	if cmd.Name != "presence" {
		t.Fatalf("unexpected name %q", cmd.Name)
	}
	hasAlias := false
	for _, a := range cmd.Aliases {
		if a == "driver" {
			hasAlias = true
		}
	}
	if !hasAlias {
		t.Errorf("presence command missing 'driver' alias: %v", cmd.Aliases)
	}
	want := map[string]bool{"list": false, "send": false, "inbox": false}
	for _, sub := range cmd.Subs {
		if _, ok := want[sub.Name]; ok {
			want[sub.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing presence sub-command %q", name)
		}
	}
}

// TestPresenceFlagBinding exercises each subcommand's Config so flag binding does
// not panic and the bound option vars are wired (mirrors the job/agent CLI tests).
func TestPresenceFlagBinding(t *testing.T) {
	cmd := NewPresenceCmd()
	for _, name := range []string{"list", "send", "inbox"} {
		sub := findSub(t, cmd, name)
		c := gcli.NewCommand(sub.Name, sub.Desc, nil)
		if sub.Config != nil {
			sub.Config(c) // must not panic
		}
	}
}

// TestPresenceSendRequiresTo verifies the send guard fires before any network I/O
// when --to is empty (so it never reaches newClient).
func TestPresenceSendRequiresTo(t *testing.T) {
	presenceSendOpts.to = ""
	if err := runPresenceSend(gcli.NewCommand("send", "", nil), nil); err == nil {
		t.Fatal("send without --to should error")
	}
}
