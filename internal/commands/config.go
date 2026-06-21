package commands

import (
	"os"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	configtmpl "github.com/inhere/gofer/config"
)

// configExitErr is the process exit code returned when `config validate` finds
// a failing check. gcli derives a non-zero exit from errorx.ErrorCoder errors
// (mirrors serveExitErr), so CI can gate on the config being valid.
const configExitErr = 2

// initOpts holds `gofer init` flags.
var initOpts = struct {
	config string
	force  bool
}{}

// configValidateOpts holds `gofer config validate` flags.
var configValidateOpts = struct {
	config string
}{}

// DefaultInitConfigPath is where `gofer init [server]` writes the starter server
// config when no --config is given: ./.gofer.yaml, the highest-priority
// current-dir candidate in the loader lookup chain (loader.go
// CurrentDirConfigNames), so a freshly generated file is picked up automatically.
const DefaultInitConfigPath = ".gofer.yaml"

// DefaultWorkerConfigPath is where `gofer init worker` writes the starter worker
// config when no --config is given. The worker config has no auto-discovery, so
// it must be passed explicitly: `gofer worker -c worker.yaml`.
const DefaultWorkerConfigPath = "worker.yaml"

// initTemplate maps an init target to its embedded starter template + default
// output path. `server` is the default (backward-compatible: bare `gofer init`).
func initTemplate(target string) (tmpl, defaultPath string, ok bool) {
	switch target {
	case "", "server":
		return configtmpl.ExampleYAML, DefaultInitConfigPath, true
	case "worker":
		return configtmpl.WorkerExampleYAML, DefaultWorkerConfigPath, true
	default:
		return "", "", false
	}
}

// NewInitCmd builds the top-level `gofer init [target]` command (E3). target is
// `server` (default) or `worker`; it writes the matching embedded starter to its
// default path (./.gofer.yaml / ./worker.yaml) or --config <path>. It refuses to
// overwrite an existing file unless --force is given (design D6).
func NewInitCmd() *gcli.Command {
	return &gcli.Command{
		Name: "init",
		Desc: "Generate a starter config from the example template (target: server | worker)",
		Config: func(c *gcli.Command) {
			c.AddArg("target", "what to scaffold: server (default) | worker", false)
			c.StrOpt(&initOpts.config, "config", "c", "", "path to write the config (default depends on target)")
			c.BoolOpt(&initOpts.force, "force", "f", false, "overwrite an existing config file")
		},
		Func: runInit,
	}
}

// runInit writes the embedded example template for the chosen target to the
// output path. The templates are the single source of truth shared with
// config/{gofer,worker}.example.yaml (no drift).
func runInit(c *gcli.Command, _ []string) error {
	target := "server"
	if a := c.Arg("target"); a != nil && a.String() != "" {
		target = strings.ToLower(a.String())
	}
	tmpl, defaultPath, ok := initTemplate(target)
	if !ok {
		return errorx.Failf(configExitErr, "unknown init target %q (use: server | worker)", target)
	}

	path := initOpts.config
	if path == "" {
		path = defaultPath
	}

	if !initOpts.force {
		if _, err := os.Stat(path); err == nil {
			// Refuse to clobber a real config (design D6); coded error → non-zero exit.
			return errorx.Failf(configExitErr, "config %s already exists; use --force to overwrite", path)
		} else if !os.IsNotExist(err) {
			return errorx.Failf(configExitErr, "stat %s: %v", path, err)
		}
	}

	if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
		return errorx.Failf(configExitErr, "write %s: %v", path, err)
	}
	if target == "worker" {
		c.Printf("已生成 worker 配置 %s，编辑后运行 `gofer worker -c %s` 启动\n", path, path)
	} else {
		c.Printf("已生成 %s，编辑后运行 `gofer config validate` 校验\n", path)
	}
	return nil
}

// NewConfigCmd builds the `config` command group. v1 exposes `config validate`,
// the global counterpart to `project validate <key>`: it loads the config via
// the lookup chain (including the structural validate(cfg) check) and runs the
// per-project filesystem/reference checks for every registered project.
func NewConfigCmd() *gcli.Command {
	return &gcli.Command{
		Name: "config",
		Desc: "Inspect and validate the gofer configuration",
		Subs: []*gcli.Command{
			{
				Name: "validate",
				Desc: "Validate the whole config: load + every project's paths/agents/runners",
				Config: func(c *gcli.Command) {
					c.StrOpt(&configValidateOpts.config, "config", "c", "", "path to the bridge config file")
				},
				Func: runConfigValidate,
			},
		},
	}
}

// runConfigValidate loads the config (which runs Load's structural validate) and
// then iterates every project running the same per-project checks as
// `project validate <key>` (reusing loadRegistry + reg.Validate, not a reimpl).
// It prints one [OK]/[FAIL] line per check. Any FAIL → coded error → non-zero
// exit; all OK → "config OK".
func runConfigValidate(c *gcli.Command, _ []string) error {
	// loadRegistry calls config.Load, which already runs validate(cfg) (host_path
	// required) and surfaces decode errors — a coded error so the exit is non-zero.
	reg, err := loadRegistry(configValidateOpts.config)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}

	keys := reg.List()
	if len(keys) == 0 {
		c.Println("(no projects registered)")
		c.Println("config OK")
		return nil
	}
	// Deterministic output order across runs (reg.List backing map iteration is
	// otherwise unordered).
	sort.Strings(keys)

	allOK := true
	for _, key := range keys {
		results, ok, vErr := reg.Validate(key)
		if vErr != nil {
			// Unknown project mid-iteration shouldn't happen (keys come from List),
			// but surface it as a failure rather than aborting the whole sweep.
			c.Printf("[FAIL] %-22s %v\n", key, vErr)
			allOK = false
			continue
		}
		for _, res := range results {
			status := "OK  "
			if !res.OK {
				status = "FAIL"
			}
			c.Printf("[%s] %s/%-18s %s\n", status, key, res.Name, res.Info)
		}
		if !ok {
			allOK = false
		}
	}

	if !allOK {
		return errorx.Failf(configExitErr, "config validation failed")
	}
	c.Println("config OK")
	return nil
}
