package servicemgr

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedRuntimeDirFollowsFrozenConfigArgument(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "cfg", "config.yaml")
	spec := Spec{SchemaVersion: SpecSchema, Backend: BackendSystemdSystem, Name: "gofer-test", Owner: "0", RunAs: "root",
		Exe: filepath.Join(root, "gofer"), WorkDir: root, ConfigFile: configFile,
		ConfigDir: filepath.Join(root, "assets"), RuntimeDir: filepath.Join(root, "assets", "run")}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "runtime_dir") {
		t.Fatalf("inconsistent runtime dir accepted: %v", err)
	}
	spec.RuntimeDir = filepath.Join(filepath.Dir(configFile), "run")
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	args, err := spec.ServerArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 3 || args[1] != "-c" || args[2] != configFile {
		t.Fatalf("managed args changed: %q", args)
	}
}
