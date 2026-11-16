//go:build linux

package servicemgr

import (
	"bytes"
	"os/user"
	"path/filepath"
	"testing"
)

// TestWorkerSystemdUnitContent: a worker unit runs `gofer worker --worker-config`,
// is a user unit (no User= line), and carries the worker's own description.
func TestWorkerSystemdUnitContent(t *testing.T) {
	_, spec := workerSpecFixture(t)
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	spec.Backend = BackendSystemdUser
	spec.Owner = current.Uid
	unit, err := unitContent(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Gofer-Managed: " + spec.Owner,
		"Description=Gofer managed worker w-t5",
		`ExecStart="`, `"worker" "--worker-config" ` + `"` + filepath.Base(spec.ConfigFile) + `"`,
		`Environment="GOFER_CONFIG_DIR=` + spec.ConfigDir,
		`Environment="GOFER_MANAGED_SPEC=` + filepath.Join(spec.ConfigDir, "run", "service", spec.Name+".json"),
		"WantedBy=default.target",
	} {
		if !bytes.Contains(unit, []byte(want)) {
			t.Errorf("worker unit missing %q: %s", want, unit)
		}
	}
	if bytes.Contains(unit, []byte("User=")) {
		t.Fatalf("worker unit must be a user unit: %s", unit)
	}
	if bytes.Contains(unit, []byte(`"serve"`)) {
		t.Fatalf("worker unit runs serve: %s", unit)
	}
}
