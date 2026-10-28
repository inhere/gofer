//go:build !windows

package worker

import "os"

func runningAsRoot() bool { return os.Geteuid() == 0 }
