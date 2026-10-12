package tracker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	yaml "github.com/goccy/go-yaml"
)

// ErrGlobalNotSynced is returned for a global copy that never synced and holds
// nothing yet: a reader should ask the server instead of showing nothing.
var ErrGlobalNotSynced = errors.New("the global memory copy has never synced")

// GlobalDirName is the directory of the global memory tracker under the gofer config
// directory: <config-dir>/tracker (docs/design/2026-10-12-local-global-memory-design.md).
const GlobalDirName = "tracker"

// OpenGlobal opens the machine's copy of the global memories at dir (normally
// <config-dir>/tracker), creating it on first use. It is an ordinary tracker store —
// same files, locking and sync state — whose config names the reserved
// GlobalTrackerID and syncs after every write.
func OpenGlobal(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("global memory directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create global memory directory: %w", err)
	}
	s := NewStore(dir)
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		b, err := yaml.Marshal(Config{TrackerID: GlobalTrackerID, AutoSync: true})
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", cfgPath, err)
		}
	} else if err != nil {
		return nil, err
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.TrackerID != GlobalTrackerID {
		return nil, fmt.Errorf("%s is not the global memory tracker (tracker_id %q)", dir, cfg.TrackerID)
	}
	return s, nil
}

// SyncedBefore reports whether this store has completed a sync with a server (its
// sync-revs state exists). A global copy that never synced holds only what was
// written on this machine.
func (s *Store) SyncedBefore() bool {
	_, ok, err := readSyncRevs(s.Dir)
	return ok && err == nil
}
