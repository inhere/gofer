package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
)

// TestParseVarFlagsAtFile covers the `@` forms of --var (2026-10-21 design「二」): the
// value can come from a file, from stdin, or be a literal that starts with '@'.
func TestParseVarFlagsAtFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brief.md")
	const body = "任务正文\nsecond line\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := parseVarFlags(gcli.Strings{"task=@" + path, "note=inline", "literal=@@at", "empty="})
	if err != nil {
		t.Fatalf("parseVarFlags: %v", err)
	}
	if got["task"] != body {
		t.Fatalf("task = %q, want the file content %q", got["task"], body)
	}
	if got["note"] != "inline" {
		t.Fatalf("note = %q, want the inline value", got["note"])
	}
	if got["literal"] != "@at" {
		t.Fatalf("literal = %q, want %q", got["literal"], "@at")
	}
	if v, ok := got["empty"]; !ok || v != "" {
		t.Fatalf("empty = %q (present=%v), want a present empty value", v, ok)
	}
}

// TestParseVarFlagsAtStdin proves `--var task=@-` reads the value from stdin.
func TestParseVarFlagsAtStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	if _, err := w.WriteString("from stdin\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	got, err := parseVarFlags(gcli.Strings{"task=@-"})
	if err != nil {
		t.Fatalf("parseVarFlags: %v", err)
	}
	if got["task"] != "from stdin\n" {
		t.Fatalf("task = %q, want the stdin content", got["task"])
	}
}

// TestParseVarFlagsAtFileErrors proves a bad `@` value names the VARIABLE it belongs to
// instead of failing with a bare os error.
func TestParseVarFlagsAtFileErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.md")
	cases := []struct {
		flag string
		want string
	}{
		{"task=@" + missing, "task"},
		{"task=@" + missing, missing},
		{"task=@" + dir, "directory"},
		{"task=@", "file path"},
	}
	for _, c := range cases {
		_, err := parseVarFlags(gcli.Strings{c.flag})
		if err == nil {
			t.Fatalf("%q: expected an error", c.flag)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%q: error = %q, want it to mention %q", c.flag, err, c.want)
		}
	}
}

// TestParseVarFlagsRejectsMalformed proves the k=v shape is still enforced.
func TestParseVarFlagsRejectsMalformed(t *testing.T) {
	for _, flag := range []string{"novalue", "=v"} {
		if _, err := parseVarFlags(gcli.Strings{flag}); err == nil {
			t.Fatalf("%q: expected an error", flag)
		}
	}
}
