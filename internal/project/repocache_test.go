package project

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

func newCountingCache(ttl time.Duration, clock *time.Time, calls *int) *RepoCache {
	return &RepoCache{
		ttl: ttl,
		now: func() time.Time { return *clock },
		scan: func(*config.Config, config.ProjectConfig) ([]RepoInfo, error) {
			*calls++
			return []RepoInfo{{RelPath: ".", Branch: "main"}}, nil
		},
	}
}

func TestRepoCache(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{}
	proj := config.ProjectConfig{HostPath: root}
	clock := time.Unix(1000, 0)
	calls := 0
	c := newCountingCache(30*time.Second, &clock, &calls)

	get := func(cfg *config.Config, key string, p config.ProjectConfig, refresh bool) {
		t.Helper()
		repos, err := c.Discover(cfg, key, p, refresh)
		if err != nil || len(repos) != 1 {
			t.Fatalf("Discover = %v, %v", repos, err)
		}
	}
	get(cfg, "p", proj, false)
	get(cfg, "p", proj, false)
	if calls != 1 {
		t.Fatalf("second call must hit the cache, scans = %d", calls)
	}
	get(cfg, "p", proj, true)
	if calls != 2 {
		t.Fatalf("refresh must bypass the cache, scans = %d", calls)
	}
	clock = clock.Add(29 * time.Second)
	get(cfg, "p", proj, false)
	if calls != 2 {
		t.Fatalf("inside the TTL must hit, scans = %d", calls)
	}
	clock = clock.Add(2 * time.Second)
	get(cfg, "p", proj, false)
	if calls != 3 {
		t.Fatalf("after the TTL must rescan, scans = %d", calls)
	}
	get(cfg, "other", proj, false)
	if calls != 4 {
		t.Fatalf("another project key has its own entry, scans = %d", calls)
	}
	get(&config.Config{}, "p", proj, false) // a reload swaps the config snapshot
	if calls != 5 {
		t.Fatalf("a new config snapshot must invalidate, scans = %d", calls)
	}
	get(cfg, "p", config.ProjectConfig{HostPath: t.TempDir()}, false) // exec path changed
	if calls != 6 {
		t.Fatalf("a changed exec path must miss, scans = %d", calls)
	}
	// Mutating a returned slice must not corrupt the cache.
	r, _ := c.Discover(cfg, "p", config.ProjectConfig{HostPath: root}, false)
	r[0].Branch = "x"
	if r2, _ := c.Discover(cfg, "p", config.ProjectConfig{HostPath: root}, false); r2[0].Branch != "main" {
		t.Fatal("cache returned a shared slice")
	}
}
