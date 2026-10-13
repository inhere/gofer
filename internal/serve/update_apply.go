package serve

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/servicemgr"
	"github.com/inhere/gofer/internal/util"
)

// managedUpgrade replaces this managed server with a downloaded binary (gofer-29gi):
// the binary itself runs `serve upgrade --no-wait`, the same path an administrator
// takes by hand, which returns once the drain accepted the upgrade. Its output goes to
// <logDir>/serve-upgrade.log.
func managedUpgrade(spec servicemgr.Spec, logDir string) httpapi.ManagedUpgradeFunc {
	return func(ctx context.Context, binary string) error {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return err
		}
		logPath := filepath.Join(logDir, "serve-upgrade.log")
		log, err := os.Create(logPath)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd := exec.CommandContext(ctx, binary, "serve", "upgrade", "--name", spec.Name, "--binary", binary, "-c", spec.ConfigFile, "--no-wait")
		cmd.Env = upgradeEnv(os.Environ(), spec.ConfigDir)
		cmd.Stdout, cmd.Stderr = log, log
		procattr.Background(cmd)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w (output: %s)", err, logPath)
		}
		return nil
	}
}

// upgradeEnv pins the config dir of the managed instance and drops the job identity
// (the server is not a job; a stray GOFER_JOB_ID would claim one).
func upgradeEnv(env []string, configDir string) []string {
	out := make([]string, 0, util.CapSum(len(env), 1))
	for _, kv := range env {
		if strings.HasPrefix(kv, config.EnvConfigDir+"=") || strings.HasPrefix(kv, "GOFER_JOB_ID=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, config.EnvConfigDir+"="+configDir)
}
