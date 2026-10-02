package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
)

const defaultReloadWait = 10 * time.Second

type reloadReceiptState struct {
	result  config.ReloadResult
	modTime time.Time
	valid   bool
}

func readReloadReceiptState(path string) reloadReceiptState {
	st, err := os.Stat(path)
	if err != nil {
		return reloadReceiptState{}
	}
	result, err := config.ReadReloadResult(path)
	if err != nil {
		return reloadReceiptState{modTime: st.ModTime()}
	}
	return reloadReceiptState{result: result, modTime: st.ModTime(), valid: true}
}

func waitForReloadResult(path string, before reloadReceiptState, timeout time.Duration) (config.ReloadResult, error) {
	if timeout <= 0 {
		timeout = defaultReloadWait
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current := readReloadReceiptState(path)
		if current.valid && current.modTime.After(before.modTime) &&
			(current.result.Rev > before.result.Rev || current.result.Error != "") {
			return current.result, nil
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
