package buildinfo

import "strings"

// Info carries linker-injected build metadata from main into lower layers.
type Info struct {
	Version   string
	GitCommit string
	BuildDate string
}

// DisplayVersion returns the compact version string shown by CLI/API surfaces.
func (i Info) DisplayVersion() string {
	version := strings.TrimSpace(i.Version)
	commit := shortCommit(strings.TrimSpace(i.GitCommit))
	if version == "" {
		return commit
	}
	if commit == "" {
		return version
	}
	return version + " (" + commit + ")"
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
