//go:build windows

package worker

func runningAsRoot() bool { return false }
