//go:build !windows

package config

// platformPersistentEnv is Windows-only: a systemd unit or a launchd job starts
// with the environment its supervisor gives it, not with a per-user persistent
// store, so there is nothing to read here (gofer-t9md).
func platformPersistentEnv() []PersistentEnvVar {
	return nil
}
