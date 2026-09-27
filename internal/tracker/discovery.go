package tracker

import (
	"fmt"
	"os"
	"path/filepath"
)

func Discover(start, explicit string) (*Store, error) {
	if explicit != "" {
		return existingStore(explicit)
	}
	cur, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(cur, ".gofer", "tracker")
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return NewStore(path), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, fmt.Errorf("no tracker found; run gofer repo init")
		}
		cur = parent
	}
}

func existingStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("tracker %s not found; run gofer repo init", abs)
	}
	return NewStore(abs), nil
}
