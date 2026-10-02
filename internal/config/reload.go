package config

// ReloadResult describes one successful configuration generation swap. Changed
// contains top-level configuration partitions whose values differ from the
// previous generation. RestartRequired contains the dotted keys classified by
// the editable policy as startup-only and changed by this reload.
type ReloadResult struct {
	Rev             int64    `json:"rev"`
	Path            string   `json:"path"`
	Changed         []string `json:"changed"`
	RestartRequired []string `json:"restart_required"`
}
