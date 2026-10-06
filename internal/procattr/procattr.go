// Package procattr holds the one platform-specific tweak every NON-interactive child
// process needs: on Windows a process without a console (a `gofer worker -d` / `serve -d`
// daemon runs detached) that starts a console program (git, claude.cmd, npx ...) makes
// Windows open a fresh console window for EVERY such child. Background suppresses that.
//
// It is the only supported way to start a background helper process (G022: a leaf
// package, imported by everyone, importing nothing of ours). A guard test
// (TestExecCallsUseBackground) fails the build of any new exec.Command call site that
// neither calls Background nor is on its allow-list.
package procattr

import "os/exec"

// Background configures cmd to run without opening a console window on Windows. It
// MERGES into an existing SysProcAttr / CreationFlags instead of replacing them and is a
// no-op on every other platform. Call it before cmd.Start/Run/Output.
//
// Do NOT use it on a detached daemon start: CREATE_NO_WINDOW and DETACHED_PROCESS are
// mutually exclusive (daemon_windows.go owns that path). Interactive children that
// should share the user's console (an editor) also must not use it.
func Background(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	background(cmd)
}
