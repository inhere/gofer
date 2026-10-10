package commands

import (
	"context"
	"os"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/focusremote"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/tracker"
)

// primeFocusTimeout caps the whole 「当前重点」 collection (design §2.4: ≤ 1s).
// primeFocusTimeout bounds the whole section. A cold `git status` on a mounted
// Windows drive (WSL / container bind mounts) alone takes ~1.1s, so 1s silently
// dropped the repository line there.
const primeFocusTimeout = 2 * time.Second

// primeUserHome locates the user-level hook configs for primeWorkHint (the
// package tests swap it so the developer's own hooks never leak into them).
var primeUserHome = os.UserHomeDir

// primeFocusGit is the git runner of the focus section (tests swap it).
var primeFocusGit tracker.GitRunner = tracker.ExecGit

// primeFocus renders the 「当前重点」 section; "" when nothing is available.
// Server data is optional: without a reachable server only local parts show.
func primeFocus(s *tracker.Store, configPath string, now time.Time) string {
	ctx, cancel := context.WithTimeout(context.Background(), primeFocusTimeout)
	defer cancel()
	src := tracker.FocusSources{Git: primeFocusGit}
	root, _ := os.Getwd()
	withServer := true
	if cfg, err := s.ReadConfig(); err == nil {
		withServer = cfg.Prime.FocusEnvEnabled()
	}
	if addr, projectKey, err := primeServerTarget(s, configPath, root); err == nil && addr != "" {
		cli := client.NewWithTimeout(addr, os.Getenv("GOFER_SERVER_TOKEN"), primeClientTimeout)
		src.Remote = focusremote.Remote(cli, serverProjectKey(cli, projectKey, root), clientPlanPrimeLimit, withServer)
	}
	return s.BuildFocus(ctx, now, src)
}

// primeWorkHint decides whether prime carries the `gofer work report` line: the
// session relay hooks are installed (repository or user level), or the server has
// open work items for this project. Without either the line is noise.
func primeWorkHint(s *tracker.Store, configPath, cwd string) bool {
	home, _ := primeUserHome()
	if hookrelay.SessionRelayInstalled(s.RepoRoot(), home) {
		return true
	}
	addr, projectKey, err := primeServerTarget(s, configPath, cwd)
	if err != nil || addr == "" {
		return false
	}
	cli := client.NewWithTimeout(addr, os.Getenv("GOFER_SERVER_TOKEN"), primeClientTimeout)
	return focusremote.HasOpenWork(cli, serverProjectKey(cli, projectKey, cwd))
}
