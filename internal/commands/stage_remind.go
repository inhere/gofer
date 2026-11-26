package commands

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
)

// The "start a flow first" hint (plan 2026-11-26-flow-progression-plan.md, F2): in a project
// whose `stage.enabled` is on (the default), creating a plan or dispatching an implementation
// job from a session that has no flow in progress prints one line at the end of the output.
// It never blocks, and a session sees it once — the marker file below remembers that.
//
// Outside a session there is nobody to remind: a person typing `gofer plan create` already
// knows what they are doing.

const (
	// stageRemindPrefix names the per-session marker files inside <config-dir>/run/.
	stageRemindPrefix = "stage-remind-"
	// stageRemindTTL prunes marker files nobody refreshed (same window as the prompt-memory
	// state: a session that has been quiet this long is gone).
	stageRemindTTL = 7 * 24 * time.Hour
)

// stageRemind prints the hint when it applies: a session, a project with the flow switch on,
// no flow in progress for this session, and not said here before.
func stageRemind(c *gcli.Command, cli *client.Client, projectKey string) {
	sid := stageSessionID(cli)
	if sid == "" {
		return
	}
	if !stageEnabledFor(projectKey) {
		return
	}
	if stageReminded(sid) {
		return
	}
	// A server that cannot answer is not a reason to nag: silence beats a wrong hint.
	if _, ok, err := stageSessionWorkItem(cli, sid); err != nil || ok {
		return
	}
	stageMarkReminded(sid)
	c.Println("hint: implementation work — start a flow first with `gofer stage start <issue>`; `gofer stage status` shows the current step and what its gate is waiting for")
}

// stageRemindAgent is the "implementation class" test for the hint after `job run`: any
// agent but the shell runner (and an unresolved agent, which says nothing about the work).
func stageRemindAgent(agent string) bool {
	return agent != "" && agent != config.BuiltinExecAgentKey
}

// stageEnabledFor reads the project's `stage.enabled` from this machine's config (the CLI
// holds no server session here). An unknown or unresolvable project is not reminded: the
// switch exists so a project can keep the hint out of its output.
func stageEnabledFor(projectKey string) bool {
	key := strings.TrimSpace(projectKey)
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil || cfg == nil {
		return false
	}
	if key == "" {
		// No -p: the project of this directory, from the config alone (no server round trip
		// for a hint that is only printed).
		config.InjectDefaultProject(cfg)
		if abs, aerr := filepath.Abs("."); aerr == nil {
			if k, _, found := project.ResolveByCwd(cfg, abs); found {
				key = k
			}
		}
	}
	if key == "" {
		return false
	}
	if _, ok := cfg.Projects[key]; !ok {
		return false
	}
	return cfg.StageEnabledFor(key)
}

func stageRemindDir() (string, bool) {
	dir, err := config.ConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", false
	}
	return filepath.Join(dir, "run"), true
}

func stageRemindMarker(sid string) (string, bool) {
	dir, ok := stageRemindDir()
	if !ok {
		return "", false
	}
	sum := sha1.Sum([]byte(sid))
	return filepath.Join(dir, fmt.Sprintf("%s%x", stageRemindPrefix, sum[:])), true
}

// stageReminded reports whether this session has already been told.
func stageReminded(sid string) bool {
	path, ok := stageRemindMarker(sid)
	if !ok {
		return true // no config dir: the hint stays quiet rather than repeating every time
	}
	_, err := os.Stat(path)
	return err == nil
}

// stageMarkReminded records the hint for this session and prunes the markers of sessions
// quiet for stageRemindTTL.
func stageMarkReminded(sid string) {
	path, ok := stageRemindMarker(sid)
	if !ok {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
	pruneStageReminders(dir, path, time.Now().Add(-stageRemindTTL))
}

func pruneStageReminders(dir, keep string, before time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), stageRemindPrefix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if path == keep {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(before) {
			_ = os.Remove(path)
		}
	}
}
