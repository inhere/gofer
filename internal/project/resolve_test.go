package project

import (
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestResolveByCwdUnique: a single project whose host_path is a prefix of the cwd
// is detected, and cwd is relativised against that root.
func TestResolveByCwdUnique(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"demo": {HostPath: root},
	}}
	// cwd == root → rel ".".
	key, rel, ok := ResolveByCwd(cfg, root)
	if !ok || key != "demo" || rel != "." {
		t.Fatalf("root cwd: got (%q,%q,%v) want (demo,.,true)", key, rel, ok)
	}
	// cwd in a subdir → rel = the subpath.
	sub := filepath.Join(root, "module", "a")
	key, rel, ok = ResolveByCwd(cfg, sub)
	if !ok || key != "demo" || rel != filepath.Join("module", "a") {
		t.Fatalf("sub cwd: got (%q,%q,%v) want (demo,module/a,true)", key, rel, ok)
	}
}

// TestResolveByCwdLongestPrefix: nested projects (one root under another) resolve
// to the DEEPER (longest-prefix) project.
func TestResolveByCwdLongestPrefix(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "nested")
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"outer": {HostPath: outer},
		"inner": {HostPath: inner},
	}}
	// cwd inside inner → inner wins (longer prefix), not outer.
	key, rel, ok := ResolveByCwd(cfg, filepath.Join(inner, "x"))
	if !ok || key != "inner" || rel != "x" {
		t.Fatalf("nested cwd: got (%q,%q,%v) want (inner,x,true)", key, rel, ok)
	}
	// cwd in outer but NOT inner → outer wins.
	key, _, ok = ResolveByCwd(cfg, filepath.Join(outer, "other"))
	if !ok || key != "outer" {
		t.Fatalf("outer-only cwd: got (%q,%v) want (outer,true)", key, ok)
	}
}

// TestResolveByCwdTie: two distinct projects with the SAME root (equal prefix
// length) → ambiguous → ok=false (user must pass -p explicitly, D7).
func TestResolveByCwdTie(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"a": {HostPath: root},
		"b": {HostPath: root},
	}}
	if _, _, ok := ResolveByCwd(cfg, filepath.Join(root, "sub")); ok {
		t.Fatal("tie (two projects, same root) must not auto-detect")
	}
}

// TestResolveByCwdNoMatch: a cwd outside every registered root → no match.
func TestResolveByCwdNoMatch(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"demo": {HostPath: root},
	}}
	if _, _, ok := ResolveByCwd(cfg, other); ok {
		t.Fatalf("cwd outside all roots must not match")
	}
	// A sibling dir sharing a string prefix but NOT a path-component boundary must
	// not match (root="/tmp/x", cwd="/tmp/x-other").
	if _, _, ok := ResolveByCwd(cfg, root+"-other"); ok {
		t.Fatalf("string-prefix-but-not-path-prefix must not match")
	}
}

// TestResolveByCwdContainerPath: the container_path 注册锚 also participates in
// matching (gofer runs in-container, so cwd may sit under container_path).
func TestResolveByCwdContainerPath(t *testing.T) {
	host := t.TempDir()
	container := t.TempDir()
	cfg := &config.Config{Projects: map[string]config.ProjectConfig{
		"demo": {HostPath: host, ContainerPath: container},
	}}
	key, rel, ok := ResolveByCwd(cfg, filepath.Join(container, "src"))
	if !ok || key != "demo" || rel != "src" {
		t.Fatalf("container cwd: got (%q,%q,%v) want (demo,src,true)", key, rel, ok)
	}
}
