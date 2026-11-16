package servicemgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workerSpecFixture builds a valid worker registration rooted in a temp dir. Its
// backend is a Windows task so the cross-platform tests can call Validate; the
// per-platform tests override it.
func workerSpecFixture(t *testing.T) (*Manager, Spec) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "worker cfg with spaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(root, DefaultWorkerName("w-t5"))
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "gofer binary")
	if err := os.WriteFile(exe, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	workerConfig := workerConfigPath(root)
	if err := os.WriteFile(workerConfig, []byte("worker_id: w-t5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, Spec{
		SchemaVersion: SpecSchema, Backend: BackendWindowsTask, Kind: KindWorker, Name: m.Name,
		Owner: "S-1-5-21-1", Exe: exe, WorkDir: root,
		ConfigFile: workerConfig, ConfigDir: root, RuntimeDir: filepath.Join(root, "run"),
		Worker: WorkerOptions{ID: "w-t5", ConfigDir: root},
	}
}

// TestLegacySpecFileReadsAsServe: a spec written before Kind existed has no kind
// field, and every such registration runs `gofer serve` (the pid file name and
// the command line stay exactly the serve ones).
func TestLegacySpecFileReadsAsServe(t *testing.T) {
	m, spec := workerSpecFixture(t)
	legacy := Spec{SchemaVersion: SpecSchema, Backend: BackendWindowsTask, Name: m.Name, Owner: spec.Owner,
		Exe: spec.Exe, WorkDir: spec.WorkDir, ConfigFile: filepath.Join(spec.ConfigDir, "config.yaml"),
		ConfigDir: spec.ConfigDir, RuntimeDir: spec.RuntimeDir, Serve: ServeOptions{NoWeb: true}}
	if err := os.MkdirAll(filepath.Dir(m.SpecPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := saveSpec(m.SpecPath(), legacy); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.SpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"kind"`) {
		t.Fatalf("empty kind must be omitted from the spec file: %s", data)
	}
	loaded, err := m.LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Kind != "" || loaded.EffectiveKind() != KindServe {
		t.Fatalf("legacy spec kind = %q (%q)", loaded.Kind, loaded.EffectiveKind())
	}
	if got := loaded.PIDFileName(); got != "serve.pid" {
		t.Fatalf("legacy pid file = %q", got)
	}
	if got := loaded.OutLogName(); got != "serve.out.log" {
		t.Fatalf("legacy out log = %q", got)
	}
	if got := loaded.ApplicationLogName(); got != "serve.log" {
		t.Fatalf("legacy application log = %q", got)
	}
	if got := loaded.Description(); got != "Gofer managed server" {
		t.Fatalf("legacy description = %q", got)
	}
	want := []string{"serve", "supervise", "--spec", m.SpecPath()}
	if got := strings.Join(loaded.SupervisorArgs(m.SpecPath()), " "); got != strings.Join(want, " ") {
		t.Fatalf("legacy supervisor args = %q", got)
	}
	args, err := loaded.CommandArgs()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 3 || args[0] != "serve" || args[1] != "-c" || args[2] != legacy.ConfigFile {
		t.Fatalf("legacy command args = %q", args)
	}
}

func TestWorkerSpecCommandAndRuntimeNames(t *testing.T) {
	m, spec := workerSpecFixture(t)
	if err := spec.CheckFiles(); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveSpec(spec); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.LoadSpec()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != spec {
		t.Fatalf("worker spec round trip: %+v", loaded)
	}
	args, err := spec.CommandArgs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"worker", "--worker-config", spec.ConfigFile}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("worker command args = %q", args)
	}
	if got := spec.PIDFileName(); got != "worker-w-t5.pid" {
		t.Fatalf("worker pid file = %q", got)
	}
	if got := spec.OutLogName(); got != "worker-w-t5.out.log" {
		t.Fatalf("worker out log = %q", got)
	}
	if got := spec.ApplicationLogName(); got != "worker-w-t5.log" {
		t.Fatalf("worker application log = %q", got)
	}
	if got := spec.Description(); got != "Gofer managed worker w-t5" {
		t.Fatalf("worker description = %q", got)
	}
	wantSupervisor := "worker supervise --spec " + m.SpecPath()
	if got := strings.Join(spec.SupervisorArgs(m.SpecPath()), " "); got != wantSupervisor {
		t.Fatalf("worker supervisor args = %q", got)
	}
}

// TestWorkerSpecRejectsServeCombinations: the kind decides which option block and
// which backend are legal, so a spec can never mix the two processes.
func TestWorkerSpecRejectsServeCombinations(t *testing.T) {
	newSpec := func() Spec {
		_, spec := workerSpecFixture(t)
		return spec
	}
	cases := []struct {
		name   string
		change func(*Spec)
		want   string
	}{
		{"unknown kind", func(s *Spec) { s.Kind = "agent" }, "unsupported service kind"},
		{"serve options", func(s *Spec) { s.Serve.Addr = "127.0.0.1:1" }, "serve options are only valid"},
		{"system unit", func(s *Spec) { s.Backend = BackendSystemdSystem }, "user-level only"},
		{"elevated task", func(s *Spec) { s.Elevated = true }, "least privilege"},
		{"reserved id", func(s *Spec) { s.Worker.ID = "server" }, "reserved"},
		{"other config dir", func(s *Spec) { s.Worker.ConfigDir = filepath.Join(s.WorkDir, "other") }, "config_dir must equal"},
		{"not worker.yaml", func(s *Spec) { s.ConfigFile = filepath.Join(s.ConfigDir, "other.yaml") }, "a worker registration runs"},
		{"worker options on serve", func(s *Spec) {
			s.Kind = KindServe
			s.ConfigFile = filepath.Join(s.ConfigDir, "config.yaml")
		}, "worker options are only valid"},
	}
	for _, tc := range cases {
		spec := newSpec()
		tc.change(&spec)
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}
