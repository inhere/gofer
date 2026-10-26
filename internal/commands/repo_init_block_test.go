package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

// TestRepoInitManagedBlockLanguage: the instruction block `repo init` writes into
// AGENTS.md follows the repository's language — the project's `language`, else
// `server.language`, else the deployment default. The machine's locale is not a
// source: the CI and many developer machines run with LANG=C.UTF-8 / LANG=en_US.
func TestRepoInitManagedBlockLanguage(t *testing.T) {
	cases := []struct {
		name   string
		config string // %[1]s is the repository root
		env    map[string]string
		want   i18n.Lang
	}{
		{
			name:   "project language wins over the server's",
			config: "server:\n  language: zh-CN\nprojects:\n  lab:\n    host_path: '%[1]s'\n    language: en\n",
			want:   i18n.EN,
		},
		{
			name:   "server language when the project names none",
			config: "server:\n  language: en\nprojects:\n  lab:\n    host_path: '%[1]s'\n",
			want:   i18n.EN,
		},
		{
			name:   "a Chinese project stays Chinese on an English-locale machine",
			config: "server:\n  language: en\nprojects:\n  lab:\n    host_path: '%[1]s'\n    language: zh-CN\n",
			env:    map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"},
			want:   i18n.ZhCN,
		},
		{
			name: "no readable config falls back to the default",
			env:  map[string]string{"LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "GOFER_LANG": "en"},
			want: i18n.Default,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			t.Setenv("GOFER_LANG", tc.env["GOFER_LANG"])
			// The real path: on macOS t.TempDir() sits under /var, a symlink to /private/var,
			// and the command's os.Getwd() reports the resolved path, so a config naming the
			// symlinked one would not match the project (ProjectForPath compares cleaned paths).
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if tc.config != "" {
				cfg := fmt.Sprintf(tc.config, filepath.ToSlash(root))
				if err := os.WriteFile(filepath.Join(root, "test-config.yaml"), []byte(cfg), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			trackerRunOK(t, root, "repo", "init", "--no-hooks", "--prefix", "lang")
			b, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			want := tracker.ManagedBlockIn(tc.want)
			if !strings.Contains(string(b), want) {
				t.Fatalf("AGENTS.md does not carry the %s block:\n%s\nwant:\n%s", tc.want, b, want)
			}
			other := i18n.EN
			if tc.want == i18n.EN {
				other = i18n.ZhCN
			}
			if strings.Contains(string(b), tracker.ManagedBlockIn(other)) {
				t.Fatalf("AGENTS.md carries the %s block as well:\n%s", other, b)
			}
			// Re-running init keeps the block, not a second copy of it.
			trackerRunOK(t, root, "repo", "init", "--no-hooks", "--prefix", "lang")
			again, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(again), tracker.BeginBlock) != 1 || !strings.Contains(string(again), want) {
				t.Fatalf("re-running init did not keep exactly one %s block:\n%s", tc.want, again)
			}
		})
	}
}
