//go:build linux

package servicemgr

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackResetsOnlyOwnedSystemdUnit(t *testing.T) {
	m, spec := linuxSpec(t)
	unitDir := t.TempDir()
	path := filepath.Join(unitDir, spec.Name+".service")
	data, err := unitContent(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUnit(path, data); err != nil {
		t.Fatal(err)
	}
	var calls []string
	backend := systemdBackend{unitDir: unitDir, run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) > 1 && args[1] == "show" {
			return []byte("LoadState=loaded\nFragmentPath=" + path + "\n"), nil
		}
		return nil, nil
	}}
	if err := prepareRollbackStartWithBackend(context.Background(), m, spec, backend); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("systemctl --user reset-failed %s.service", spec.Name)
	if len(calls) != 2 || calls[1] != want {
		t.Fatalf("rollback systemd calls = %q, want final %q", calls, want)
	}
	if err := writeUnit(path, []byte("[Service]\nExecStart=/foreign\n")); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := prepareRollbackStartWithBackend(context.Background(), m, spec, backend); err == nil {
		t.Fatal("foreign unit was reset")
	}
	if len(calls) != 0 {
		t.Fatalf("foreign unit triggered native call: %q", calls)
	}
}
