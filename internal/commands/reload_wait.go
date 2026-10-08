package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
)

const defaultReloadWait = 10 * time.Second

// waitForReloadResult polls path until a receipt whose reloaded_at is not
// earlier than since (the time the reload signal was sent) appears. It does not
// use Rev: Rev restarts from its initial value whenever the process restarts, so
// a fresh receipt could look older than the one it replaced.
func waitForReloadResult(path string, since time.Time, timeout time.Duration) (config.ReloadResult, error) {
	if timeout <= 0 {
		timeout = defaultReloadWait
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if result, err := config.ReadReloadResult(path); err == nil && !result.ReloadedAt.Before(since) {
			return result, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return config.ReloadResult{}, fmt.Errorf("reload result timeout after %s; inspect %s", timeout, path)
}

func printReloadResult(c *gcli.Command, result config.ReloadResult) {
	line := fmt.Sprintf("rev=%d path=%s changed=%s restart_required=%s", result.Rev, result.Path, strings.Join(result.Changed, ","), strings.Join(result.RestartRequired, ","))
	if result.Error != "" {
		line += " error=" + result.Error
	}
	c.Printf("gofer: config reload %s\n", line)
}

func workerReloadResultFile(id string) string {
	return config.RuntimeFilePath("run", "worker-"+id+".reload.json")
}
