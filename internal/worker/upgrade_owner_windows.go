//go:build windows

package worker

import "os/user"

func currentUpgradeUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}

func upgradeDirOwner(string) string { return "unavailable on Windows" }
