package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/servicemgr"
)

// workerServiceOpts holds `gofer worker service ...` flags. The registration
// always manages the user-level worker config <config-dir>/worker.yaml, so there
// is deliberately no --worker-config here: a worker service is "this machine joins
// as this worker", which is exactly that file.
var workerServiceOpts struct {
	id     string
	start  bool
	purge  bool
	asJSON bool
}

// workerServiceState is the platform-neutral status of a worker registration. Its
// exit codes reuse the `serve status` scale (0 running, 3 stopped, 4 degraded,
// 5 unconfigured).
type workerServiceState struct {
	Name        string
	Registered  bool
	Active      bool
	Verified    bool
	PID         int
	NativeState string
	Detail      string
}

// NewWorkerServiceCmd builds `gofer worker service`: register, start, stop, status
// and uninstall the OS registration that keeps this host's worker running after
// logout (systemd --user on Linux, a logon task on Windows).
func NewWorkerServiceCmd() *gcli.Command {
	register := &gcli.Command{
		Name: "register", Desc: "Register this host's worker as a user-level OS service (does not start it)",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.StrOpt(&workerServiceOpts.id, "id", "", "", "worker id (default: worker_id from worker.yaml)")
			c.BoolOpt(&workerServiceOpts.start, "start", "", false, "start after registration")
		},
		Func: runWorkerServiceRegister,
	}
	start := &gcli.Command{Name: "start", Desc: "Start the registered worker service", Config: bindWorkerServiceID,
		Func: func(c *gcli.Command, _ []string) error { return runWorkerServiceAction(c, "start") }}
	stop := &gcli.Command{Name: "stop", Desc: "Stop the registered worker service", Config: bindWorkerServiceID,
		Func: func(c *gcli.Command, _ []string) error { return runWorkerServiceAction(c, "stop") }}
	status := &gcli.Command{Name: "status", Desc: "Inspect the registered worker service", Config: func(c *gcli.Command) {
		bindWorkerServiceID(c)
		c.BoolOpt(&workerServiceOpts.asJSON, "json", "", false, "print JSON")
		// The verdict is already in the output (state=...) and the exit code; the
		// default handler would print the coded error again (and corrupt --json).
		c.On(gcli.EvtCmdRunError, func(*gcli.HookCtx) bool { return true })
	}, Func: runWorkerServiceStatus}
	uninstall := &gcli.Command{Name: "uninstall", Desc: "Stop and remove the OS registration", Config: func(c *gcli.Command) {
		bindWorkerServiceID(c)
		c.BoolOpt(&workerServiceOpts.purge, "purge", "", false, "also delete worker.yaml, the GOFER_WORKER_TOKEN .env line and run/worker-<id>.*")
	}, Func: func(c *gcli.Command, _ []string) error { return runWorkerServiceAction(c, "uninstall") }}
	return &gcli.Command{Name: "service", Desc: "Manage the OS-level registration that keeps this host's worker running",
		Config: bindWorkerServiceID,
		Subs:   []*gcli.Command{register, start, stop, status, uninstall}}
}

func bindWorkerServiceID(c *gcli.Command) {
	bindConfigFlag(c)
	c.StrOpt(&workerServiceOpts.id, "id", "", "", "worker id (default: worker_id from worker.yaml)")
}

// workerServiceID resolves --id, falling back to the worker.yaml worker_id so the
// single-worker host needs no flag.
func workerServiceID(configDir string) (string, error) {
	id := strings.TrimSpace(workerServiceOpts.id)
	if id != "" {
		return id, nil
	}
	wc, err := loadWorkerConfig(filepath.Join(configDir, "worker.yaml"))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(wc.WorkerID) == "" {
		return "", fmt.Errorf("%s has no worker_id; pass --id", filepath.Join(configDir, "worker.yaml"))
	}
	return strings.TrimSpace(wc.WorkerID), nil
}

func workerServiceManager(configDir, id string) (*servicemgr.Manager, error) {
	return servicemgr.NewManager(configDir, servicemgr.DefaultWorkerName(id))
}

func runWorkerServiceRegister(c *gcli.Command, _ []string) error {
	configDir, err := config.ConfigDir()
	if err != nil {
		return err
	}
	id, err := workerServiceID(configDir)
	if err != nil {
		return err
	}
	if err := workerServiceRefuseRoot(); err != nil {
		return err
	}
	m, err := workerServiceManager(configDir, id)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return err
	}
	spec := servicemgr.Spec{SchemaVersion: servicemgr.SpecSchema, Kind: servicemgr.KindWorker, Name: m.Name,
		Exe: filepath.Clean(exe), WorkDir: configDir,
		ConfigFile: filepath.Join(configDir, "worker.yaml"), ConfigDir: configDir,
		RuntimeDir: filepath.Join(configDir, "run"),
		Worker:     servicemgr.WorkerOptions{ID: id, ConfigDir: configDir}}
	if err := workerServiceSpecDefaults(&spec); err != nil {
		return err
	}
	if err := spec.CheckFiles(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 190*time.Second)
	defer cancel()
	if err := workerServiceRegisterNative(ctx, m, spec, workerServiceOpts.start); err != nil {
		return err
	}
	c.Printf("registered %s (%s)\n", m.Name, spec.Backend)
	if workerServiceOpts.start {
		c.Printf("started %s\n", m.Name)
	}
	return nil
}

// workerServiceForAction loads the registration a start/stop/status/uninstall
// addresses, so every action reports the same "not registered" error.
func workerServiceForAction() (*servicemgr.Manager, servicemgr.Spec, error) {
	configDir, err := config.ConfigDir()
	if err != nil {
		return nil, servicemgr.Spec{}, err
	}
	id, err := workerServiceID(configDir)
	if err != nil {
		return nil, servicemgr.Spec{}, err
	}
	m, err := workerServiceManager(configDir, id)
	if err != nil {
		return nil, servicemgr.Spec{}, err
	}
	spec, err := m.LoadSpec()
	if errors.Is(err, os.ErrNotExist) {
		return m, spec, fmt.Errorf("%s is not registered; run gofer worker service register", m.Name)
	}
	if err != nil {
		return m, spec, err
	}
	return m, spec, nil
}

func runWorkerServiceAction(c *gcli.Command, action string) error {
	m, spec, err := workerServiceForAction()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 190*time.Second)
	defer cancel()
	switch action {
	case "start":
		err = workerServiceStartNative(ctx, m)
	case "stop":
		err = workerServiceStopNative(ctx, m)
	case "uninstall":
		err = workerServiceUninstallNative(ctx, m)
		if err == nil && workerServiceOpts.purge {
			var removed []string
			removed, err = purgeWorkerAssets(spec.ConfigDir, spec.Worker.ID)
			for _, path := range removed {
				c.Printf("removed %s\n", path)
			}
		}
	default:
		return errors.New("unknown worker service action")
	}
	if err != nil {
		return err
	}
	c.Printf("%s %s\n", action, m.Name)
	return nil
}

func runWorkerServiceStatus(c *gcli.Command, _ []string) error {
	configDir, err := config.ConfigDir()
	if err != nil {
		return serveStatusCheckError(err)
	}
	id, err := workerServiceID(configDir)
	if err != nil {
		return serveStatusCheckError(err)
	}
	m, err := workerServiceManager(configDir, id)
	if err != nil {
		return serveStatusCheckError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// An unregistered worker is a status verdict (unconfigured), not a failed
	// check: the platform reports it through the state.
	state, err := workerServiceStatusNative(ctx, m)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return serveStatusCheckError(err)
	}
	if errors.Is(err, os.ErrNotExist) {
		state = workerServiceState{Name: m.Name}
	}
	name, code := classifyWorkerService(state)
	if workerServiceOpts.asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(workerServiceJSON{workerServiceState: state, Verdict: name, ExitCode: code}); err != nil {
			return err
		}
		return serveStatusExitError(name, code)
	}
	c.Printf("name=%s registered=%t native=%s active=%t pid=%d verified=%t\n",
		state.Name, state.Registered, state.NativeState, state.Active, state.PID, state.Verified)
	if state.Detail != "" {
		c.Printf("detail: %s\n", state.Detail)
	}
	c.Printf("state=%s\n", name)
	return serveStatusExitError(name, code)
}

type workerServiceJSON struct {
	workerServiceState
	Verdict  string `json:"state"`
	ExitCode int    `json:"exit_code"`
}

// classifyWorkerService maps the platform status onto the `serve status` states
// and exit codes: 0 running, 3 stopped, 4 degraded, 5 unconfigured.
func classifyWorkerService(state workerServiceState) (string, int) {
	if !state.Registered {
		return serveStatusStateUnconfig, serveStatusExitUnconfig
	}
	if !state.Active {
		return serveStatusStateDown, serveStatusExitDown
	}
	if !state.Verified {
		return serveStatusStateDegraded, serveStatusExitDegraded
	}
	return serveStatusStateOK, serveStatusExitOK
}

// purgeWorkerAssets deletes exactly the files that make this host a worker of this
// id: worker.yaml, the GOFER_WORKER_TOKEN lines in .env, and run/worker-<id>.*.
// Everything else in the config directory is preserved.
func purgeWorkerAssets(configDir, id string) ([]string, error) {
	var removed []string
	workerConfig := filepath.Join(configDir, "worker.yaml")
	if err := os.Remove(workerConfig); err == nil {
		removed = append(removed, workerConfig)
	} else if !errors.Is(err, os.ErrNotExist) {
		return removed, err
	}
	dotenv := filepath.Join(configDir, ".env")
	data, err := os.ReadFile(dotenv)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return removed, err
	}
	if err == nil {
		kept, dropped := dropTokenLines(string(data))
		if dropped {
			if err := os.WriteFile(dotenv, []byte(kept), 0o600); err != nil {
				return removed, err
			}
			removed = append(removed, dotenv+" (GOFER_WORKER_TOKEN)")
		}
	}
	matches, err := filepath.Glob(filepath.Join(configDir, "run", "worker-"+id+".*"))
	if err != nil {
		return removed, err
	}
	sort.Strings(matches)
	for _, path := range matches {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, path)
	}
	return removed, nil
}

// dropTokenLines removes every GOFER_WORKER_TOKEN assignment (with or without a
// leading `export`) and reports whether it dropped any.
func dropTokenLines(text string) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		key := strings.TrimSpace(line)
		key = strings.TrimPrefix(key, "export ")
		if name, _, ok := strings.Cut(key, "="); ok && strings.TrimSpace(name) == "GOFER_WORKER_TOKEN" {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, ""), dropped
}
