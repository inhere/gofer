package config

import (
	"strings"
	"testing"
)

// TestProjectKeyFromDir covers the four `--project auto` primary-source outcomes
// (xu64.15): a file with key: / a file without key: / no file / a bad yaml.
func TestProjectKeyFromDir(t *testing.T) {
	t.Run("has key (trimmed)", func(t *testing.T) {
		dir := t.TempDir()
		writeOverlay(t, dir, "key: \"  demo  \"\ndefault_agent: claude\n")
		got, err := ProjectKeyFromDir(dir)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != "demo" {
			t.Fatalf("key = %q, want demo", got)
		}
	})

	t.Run("file without key -> empty", func(t *testing.T) {
		dir := t.TempDir()
		writeOverlay(t, dir, "default_agent: claude\n")
		got, err := ProjectKeyFromDir(dir)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != "" {
			t.Fatalf("key = %q, want empty", got)
		}
	})

	t.Run("no file -> empty, no error", func(t *testing.T) {
		got, err := ProjectKeyFromDir(t.TempDir())
		if err != nil {
			t.Fatalf("missing file must not error, got: %v", err)
		}
		if got != "" {
			t.Fatalf("key = %q, want empty", got)
		}
	})

	t.Run("bad yaml -> error", func(t *testing.T) {
		dir := t.TempDir()
		writeOverlay(t, dir, "key: [unclosed\n")
		if _, err := ProjectKeyFromDir(dir); err == nil {
			t.Fatal("expected decode error, got nil")
		}
	})
}

// TestOverlayKeyNotForbidden verifies that a key: bearing overlay produces NO
// forbidden-key warning — key is a self-identifier, absent from the blacklist
// (there is no allow-list to extend), and is NOT merged into ProjectConfig.
func TestOverlayKeyNotForbidden(t *testing.T) {
	warns := detectForbiddenOverlayKeys("demo", []byte("key: demo\ndefault_agent: claude\n"))
	for _, w := range warns {
		if strings.Contains(w, `"key"`) {
			t.Fatalf("key: must not be flagged forbidden, got warn: %s", w)
		}
	}
	// key: does not leak into the merged ProjectConfig (decode-only).
	merged := MergeProjectConfig(ProjectConfig{DefaultAgent: "x"}, ProjectOverlay{Key: strPtr("demo")})
	if merged.DefaultAgent != "x" {
		t.Fatalf("Key overlay must not change ProjectConfig, got DefaultAgent=%q", merged.DefaultAgent)
	}
}

// TestProjectForPath covers the standalone path-match fallback: hit, longest
// prefix (nested), equal-length ambiguity, no match, and the path_view=container
// branch (ExecPath picks container_path).
func TestProjectForPath(t *testing.T) {
	host := func(projects map[string]ProjectConfig) *Config {
		return &Config{Projects: projects}
	}

	t.Run("simple hit (equal + below)", func(t *testing.T) {
		c := host(map[string]ProjectConfig{
			"demo": {HostPath: "/root/demo"},
			"other": {HostPath: "/root/other"},
		})
		for _, cwd := range []string{"/root/demo", "/root/demo/sub/x"} {
			if k, ok := c.ProjectForPath(cwd); !ok || k != "demo" {
				t.Fatalf("cwd %q: got (%q,%v), want (demo,true)", cwd, k, ok)
			}
		}
	})

	t.Run("longest prefix wins (nested)", func(t *testing.T) {
		c := host(map[string]ProjectConfig{
			"outer": {HostPath: "/root/demo"},
			"inner": {HostPath: "/root/demo/inner"},
		})
		if k, ok := c.ProjectForPath("/root/demo/inner/x"); !ok || k != "inner" {
			t.Fatalf("nested: got (%q,%v), want (inner,true)", k, ok)
		}
		if k, ok := c.ProjectForPath("/root/demo/other"); !ok || k != "outer" {
			t.Fatalf("outer-only: got (%q,%v), want (outer,true)", k, ok)
		}
	})

	t.Run("equal-length ambiguity -> not ok", func(t *testing.T) {
		// Two DISTINCT registered paths of equal length both matching the same cwd is
		// impossible for a real fs, but ProjectForPath must still refuse to guess when
		// two equal-length ExecPaths tie. Use two dirs that both prefix cwd at equal len.
		c := host(map[string]ProjectConfig{
			"a": {HostPath: "/root/aa"},
			"b": {HostPath: "/root/aa"}, // same length, same value -> ambiguous
		})
		if k, ok := c.ProjectForPath("/root/aa/x"); ok {
			t.Fatalf("ambiguous must be !ok, got (%q,%v)", k, ok)
		}
	})

	t.Run("no match -> not ok", func(t *testing.T) {
		c := host(map[string]ProjectConfig{"demo": {HostPath: "/root/demo"}})
		if k, ok := c.ProjectForPath("/elsewhere"); ok {
			t.Fatalf("no match must be !ok, got (%q,%v)", k, ok)
		}
	})

	t.Run("empty ExecPath skipped", func(t *testing.T) {
		c := host(map[string]ProjectConfig{"blank": {HostPath: ""}, "demo": {HostPath: "/root/demo"}})
		if k, ok := c.ProjectForPath("/root/demo"); !ok || k != "demo" {
			t.Fatalf("got (%q,%v), want (demo,true)", k, ok)
		}
	})

	t.Run("path_view=container uses container_path", func(t *testing.T) {
		c := &Config{
			Server:   ServerConfig{PathView: "container"},
			Projects: map[string]ProjectConfig{"demo": {HostPath: "/host/demo", ContainerPath: "/work/demo"}},
		}
		// container view: matches container_path, NOT host_path.
		if k, ok := c.ProjectForPath("/work/demo/x"); !ok || k != "demo" {
			t.Fatalf("container cwd: got (%q,%v), want (demo,true)", k, ok)
		}
		if _, ok := c.ProjectForPath("/host/demo/x"); ok {
			t.Fatal("host path must NOT match under path_view=container")
		}
	})
}
