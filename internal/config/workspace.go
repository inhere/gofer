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
