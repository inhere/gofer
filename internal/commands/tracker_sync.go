package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

func tryAutoSync(c *gcli.Command, s *tracker.Store) {
	_ = c
	cfg, err := s.ReadConfig()
	if err != nil || !cfg.AutoSync {
		return
	}
	cli, err := trackerClient()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report, err := tracker.SyncHTTPWithToken(ctx, s, strings.TrimRight(cli.BaseURL(), "/"), cli.Token())
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync warning:", err)
		return
	}
	if report.RepairedToServer > 0 || report.RepairedToLocal > 0 || len(report.Unresolved) > 0 {
		fmt.Fprintln(os.Stderr, "sync:", report.Summary)
	}
}

func trackerClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, os.Getenv("GOFER_SERVER_ADDR"), os.Getenv("GOFER_SERVER_TOKEN"))
}

// globalMemoryStore opens this machine's copy of the global memories
// (<config-dir>/tracker, gofer-j6qn). A copy that never synced pulls the server's
// memories first (best effort), so a new machine does not start empty.
func globalMemoryStore() (*tracker.Store, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate the gofer config directory: %w", err)
	}
	s, err := tracker.OpenGlobal(filepath.Join(dir, tracker.GlobalDirName))
	if err != nil {
		return nil, err
	}
	if !s.SyncedBefore() {
		if _, err := syncGlobalMemories(s, 5*time.Second); err != nil && !errors.Is(err, errNoServer) {
			fmt.Fprintln(os.Stderr, "global memories: first sync with the server failed (using the local copy):", err)
		}
	}
	return s, nil
}

// errNoServer is a sync that had no server to talk to.
var errNoServer = errors.New("no gofer server configured")

// syncGlobalMemories syncs the global copy with the server within timeout.
func syncGlobalMemories(s *tracker.Store, timeout time.Duration) (tracker.SyncReport, error) {
	cli, err := trackerClient()
	if err != nil || cli.BaseURL() == "" {
		return tracker.SyncReport{}, errNoServer
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return tracker.SyncHTTPWithToken(ctx, s, strings.TrimRight(cli.BaseURL(), "/"), cli.Token())
}

// jobCredentialEnv reports whether this CLI runs inside a gofer job (its credential
// is the job's own).
func jobCredentialEnv() bool { return strings.TrimSpace(os.Getenv(config.EnvJobToken)) != "" }
