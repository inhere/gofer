package commands

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/gcli/v3/gflag"

	"github.com/inhere/gofer/internal/i18n"
)

// cliHelpGolden is the zh-CN help text of the commands migrated to the CLI
// catalog (phase 2 of docs/design/2026-10-27-i18n-design.md §6). It was generated
// from the code BEFORE the migration (GOFER_GOLDEN_UPDATE=1) and must stay
// byte-identical: an existing zh-CN deployment keeps exactly the help it had.
var cliHelpGolden = filepath.Join("testdata", "cli-help-zh-CN.golden")

// migratedHelpRoots are the command subtrees whose help text this task migrated.
var migratedHelpRoots = []string{"job", "plan", "session"}

// TestZhCliHelpIsUnchanged pins the zh-CN help text (command descriptions, help,
// examples and every flag usage) of the migrated subtrees.
func TestZhCliHelpIsUnchanged(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.ZhCN))
	t.Setenv(i18n.EnvClientLangVar, "") // the client config never speaks for this test
	got := collectCliHelp(t)

	if os.Getenv("GOFER_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cliHelpGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(cliHelpGolden)
	if err != nil {
		t.Fatalf("read golden (regenerate with GOFER_GOLDEN_UPDATE=1): %v", err)
	}
	if got != string(want) {
		t.Errorf("zh-CN help text changed (regenerate with GOFER_GOLDEN_UPDATE=1 only when the change is intended)\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// collectCliHelp renders every help text of the migrated subtrees, one
// "<command path>\t<kind>\t<text>" line each, in a stable order.
func collectCliHelp(t *testing.T) string {
	t.Helper()
	app := NewApp("test")
	var lines []string
	var walk func(cmd *gcli.Command, path string)
	walk = func(cmd *gcli.Command, path string) {
		cmd.Init()
		lines = append(lines, path+"\tdesc\t"+cmd.Desc)
		lines = append(lines, path+"\thelp\t"+cmd.Help)
		lines = append(lines, path+"\texamples\t"+cmd.Examples)
		var flags []string
		cmd.Flags.FSet().VisitAll(func(f *gflag.Flag) {
			flags = append(flags, path+"\tflag "+f.Name+"\t"+f.Usage)
		})
		sort.Strings(flags)
		lines = append(lines, flags...)
		names := make([]string, 0, len(cmd.Commands()))
		for name := range cmd.Commands() {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			walk(cmd.Commands()[name], path+" "+strings.TrimSpace(name))
		}
	}
	for _, root := range migratedHelpRoots {
		cmd, ok := app.Command(root)
		if !ok {
			t.Fatalf("command %q not found", root)
		}
		walk(cmd, root)
	}
	return strings.Join(lines, "\n") + "\n"
}
