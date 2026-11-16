package servicemgr

import (
	"path/filepath"
	"regexp"
)

// Kind selects the Gofer process a managed registration runs. A spec file
// written before Kind existed has no kind field; those registrations all run
// `gofer serve`, which EffectiveKind reports for them.
type Kind string

const (
	KindServe  Kind = "serve"
	KindWorker Kind = "worker"
)

// workerID is the worker-id shape a managed registration accepts. It is the
// same alphabet as a service name; the reserved-name rule comes from
// config.CheckWorkerID.
var workerID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// WorkerOptions is the worker-specific part of a managed registration. ConfigDir
// is the worker's own config directory (worker.yaml, .env and run/ inside it); it
// must equal the spec's ConfigDir because the managed process is started with
// GOFER_CONFIG_DIR pointing there.
type WorkerOptions struct {
	ID        string `json:"id,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"`
}

// EffectiveKind reads a legacy spec (no kind field) as serve.
func (s Spec) EffectiveKind() Kind {
	if s.Kind == "" {
		return KindServe
	}
	return s.Kind
}

// DefaultWorkerName is the default managed name of a worker registration. Worker
// names carry the id so several workers on one host never collide.
func DefaultWorkerName(id string) string { return "gofer-worker-" + id }

// PIDFileName is the runtime pidfile of the managed process: serve.pid for a
// server (its long-standing name) and worker-<id>.pid for a worker, matching the
// daemon-mode file names of `gofer worker -d`.
func (s Spec) PIDFileName() string {
	if s.EffectiveKind() == KindWorker {
		return "worker-" + s.Worker.ID + ".pid"
	}
	return "serve.pid"
}

// OutLogName is where a Windows supervisor redirects the managed process output.
func (s Spec) OutLogName() string {
	if s.EffectiveKind() == KindWorker {
		return "worker-" + s.Worker.ID + ".out.log"
	}
	return "serve.out.log"
}

// ApplicationLogName is the fallback application log in RuntimeDir, used when the
// managed config names no log file or directory.
func (s Spec) ApplicationLogName() string {
	if s.EffectiveKind() == KindWorker {
		return "worker-" + s.Worker.ID + ".log"
	}
	return "serve.log"
}

// SupervisorArgs is the hidden command a Windows task starts. The same entry
// serves both kinds: it loads the spec and supervises whatever Kind it names, so
// a worker registration needs no separate hidden command.
func (s Spec) SupervisorArgs(specPath string) []string {
	return []string{"serve", "supervise", "--spec", specPath}
}

// CommandArgs is the managed process command line, built per kind from an
// allowlist only. A worker registration runs `gofer worker --worker-config
// <worker.yaml>`; its hub, projects and runner policy all live in that file.
func (s Spec) CommandArgs() ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.EffectiveKind() == KindWorker {
		return []string{"worker", "--worker-config", s.ConfigFile}, nil
	}
	return s.ServerArgs()
}

// Description is the human-readable unit/task description for the kind.
func (s Spec) Description() string {
	if s.EffectiveKind() == KindWorker {
		return "Gofer managed worker " + s.Worker.ID
	}
	return "Gofer managed server"
}

// workerConfigPath is the worker.yaml a managed worker registration runs.
func workerConfigPath(configDir string) string { return filepath.Join(configDir, "worker.yaml") }
