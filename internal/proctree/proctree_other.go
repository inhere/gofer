//go:build !unix && !windows

package proctree

import "os/exec"

// Tree is a no-op containment on the remaining platforms (plan9, js/wasm): there is no
// portable way to kill a tree, so the caller degrades to killing the direct child —
// exactly what it did before this package existed.
type Tree struct{}

// New returns an inert containment.
func New() *Tree { return &Tree{} }

// Configure does nothing.
func (t *Tree) Configure(*exec.Cmd) {}

// Attach does nothing.
func (t *Tree) Attach(*exec.Cmd) error { return nil }

// Kill does nothing (the platform has no process-tree primitive this package uses).
func (t *Tree) Kill() {}

// Release does nothing.
func (t *Tree) Release() {}

// Alive reports false: this platform has no portable liveness probe here.
func Alive(int) bool { return false }
