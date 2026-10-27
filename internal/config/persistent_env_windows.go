//go:build windows

package config

import (
	"log/slog"

	"golang.org/x/sys/windows/registry"
)

// The two persistent environment stores a Windows service inherits: the task
// scheduler starts the service for an account, whose process environment is the
// Machine variables with the User ones on top.
const (
	machineEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvKey    = `Environment`
)

// platformPersistentEnv reads both stores (gofer-t9md). A value here overrides
// <config-dir>/.env for the service: LoadDotenv keeps an already-set variable,
// so an old GOFER_TOKEN left in the User environment shadows the file the
// operator edits.
func platformPersistentEnv() []PersistentEnvVar {
	out := make([]PersistentEnvVar, 0, 16)
	out = append(out, readPersistentEnv(registry.LOCAL_MACHINE, machineEnvKey, PersistentEnvScopeMachine)...)
	out = append(out, readPersistentEnv(registry.CURRENT_USER, userEnvKey, PersistentEnvScopeUser)...)
	return out
}

// readPersistentEnv lists one store. An unreadable store (permissions, a
// non-Windows-served account) contributes nothing: the diagnosis degrades to the
// dotenv-only one instead of failing `serve status`.
func readPersistentEnv(root registry.Key, path, scope string) []PersistentEnvVar {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		slog.Debug("token: cannot read the persistent environment", "scope", scope, "error", err)
		return nil
	}
	defer key.Close()
	names, err := key.ReadValueNames(-1)
	if err != nil {
		slog.Debug("token: cannot list the persistent environment", "scope", scope, "error", err)
		return nil
	}
	out := make([]PersistentEnvVar, 0, len(names))
	for _, name := range names {
		// Only string values are environment variables; the type filter drops a
		// REG_DWORD / binary entry that happens to live in the same key.
		val, _, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		out = append(out, PersistentEnvVar{Scope: scope, Name: name, Value: val})
	}
	return out
}
