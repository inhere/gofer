//go:build !windows

package worker

import (
	"os/user"
	"strconv"
	"syscall"
)

func currentUpgradeUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}

func upgradeDirOwner(dir string) string {
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		return "unknown"
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(uid); err == nil && u.Username != "" {
		return u.Username
	}
	return "uid " + uid
}
