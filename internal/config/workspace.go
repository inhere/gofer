package config

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvWorkspace overrides the default workspace directory (F-g). It is the
// env-var form of `gofer init --workspace`; an explicit flag still wins.
const EnvWorkspace = "GOFER_WORKSPACE"

// DefaultWorkspaceDirName / DefaultWorkspaceSubdir compose the workspace path under
// the user's home: ~/.gofer/workspace. Short and identical on every platform (the
// design settled on ~/.gofer rather than ~/.local/gofer, which is not a Windows
// convention); os.UserHomeDir resolves %USERPROFILE% on Windows.
const (
	DefaultWorkspaceDirName = ".gofer"
	DefaultWorkspaceSubdir  = "workspace"
)

// WorkspaceDir resolves the default workspace directory: an explicit path (the
// --workspace flag) wins, then GOFER_WORKSPACE, then ~/.gofer/workspace. The path is
// returned absolute but is NOT created — callers that own the directory (the init
// commands) decide whether to mkdir.
func WorkspaceDir(explicit string) (string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		return filepath.Abs(p)
	}
	if p := strings.TrimSpace(os.Getenv(EnvWorkspace)); p != "" {
		return filepath.Abs(p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DefaultWorkspaceDirName, DefaultWorkspaceSubdir), nil
}

// EnsureWorkspaceDir makes sure the default workspace directory (WorkspaceDir(""))
// exists. It exists for processes that were upgraded in place: `gofer init`
// creates the directory, a long-installed serve/worker never did, so the Runners
// page flagged the missing directory forever. It returns the resolved path and
// whether this call created it; an existing directory is left untouched. Callers
// treat an error as a warning only — a read-only home must not stop the process.
func EnsureWorkspaceDir() (dir string, created bool, err error) {
	dir, err = WorkspaceDir("")
	if err != nil {
		return "", false, err
	}
	if fi, statErr := os.Stat(dir); statErr == nil && fi.IsDir() {
		return dir, false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, false, err
	}
	return dir, true, nil
}
