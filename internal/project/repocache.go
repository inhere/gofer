package project

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// DefaultRepoCacheTTL is how long a DiscoverRepos result is reused.
const DefaultRepoCacheTTL = 30 * time.Second

// RepoCache memoises DiscoverRepos per project. The scan walks the whole project tree
// and runs git per repo (hundreds of ms on a big workspace) while the Projects and New
// job pages both ask on every open. An entry is keyed by project key + resolved exec
// path and is dropped when the TTL passes or the config snapshot it was built from is
// replaced (a reload / project edit). The zero value is ready to use.
type RepoCache struct {
	mu      sync.Mutex
	ttl     time.Duration // 0 = DefaultRepoCacheTTL
	now     func() time.Time
	scan    func(*config.Config, config.ProjectConfig) ([]RepoInfo, error)
	entries map[string]repoCacheEntry
}

type repoCacheEntry struct {
	cfg   *config.Config
	at    time.Time
	repos []RepoInfo
}

// Discover returns the cached repos of project key, or scans (and caches) when there is
// no fresh entry or refresh is true. The returned slice is a copy.
func (c *RepoCache) Discover(cfg *config.Config, key string, proj config.ProjectConfig, refresh bool) ([]RepoInfo, error) {
	root, err := filepath.Abs(cfg.ExecPath(proj))
	if err != nil {
		return nil, err
	}
	ck := key + "\x00" + root

	c.mu.Lock()
	defer c.mu.Unlock()
	now, scan, ttl := time.Now, DiscoverRepos, c.ttl
	if c.now != nil {
		now = c.now
	}
	if c.scan != nil {
		scan = c.scan
	}
	if ttl <= 0 {
		ttl = DefaultRepoCacheTTL
	}
	if e, ok := c.entries[ck]; ok && !refresh && e.cfg == cfg && now().Sub(e.at) < ttl {
		return append([]RepoInfo{}, e.repos...), nil
	}
	repos, err := scan(cfg, proj)
	if err != nil {
		return nil, err
	}
	if c.entries == nil {
		c.entries = map[string]repoCacheEntry{}
	}
	// Entries of an older config snapshot can never hit again: drop them now.
	for k, e := range c.entries {
		if e.cfg != cfg {
			delete(c.entries, k)
		}
	}
	c.entries[ck] = repoCacheEntry{cfg: cfg, at: now(), repos: repos}
	return append([]RepoInfo{}, repos...), nil
}
