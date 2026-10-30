package commands

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The remaining command groups (every top-level command that `job` / `plan` /
// `session` and the setup & config group do not cover) migrate to the CLI catalog
// in this step (docs/design/2026-10-27-i18n-design.md §11 S2c). Both goldens were
// generated from the code BEFORE that migration (GOFER_GOLDEN_UPDATE=1), so an
// existing zh-CN deployment keeps exactly the help and the messages it had.
var (
	restHelpGolden    = filepath.Join("testdata", "cli-help-zh-CN.rest.golden")
	restOutputsGolden = filepath.Join("testdata", "cli-rest-output-zh-CN.golden")
)

// restHelpRoots are the command subtrees this step migrates.
var restHelpRoots = []string{
	"agent", "auth", "hook", "issue", "mcp", "memory", "repo", "schedule",
	"settings", "steward", "template", "tool", "tunnel", "work", "workflow", "doctor",
}

// TestZhRestCliHelpIsUnchanged pins the zh-CN help text (descriptions, help,
// examples and every flag usage) of the remaining subtrees.
func TestZhRestCliHelpIsUnchanged(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.ZhCN))
	compareGolden(t, restHelpGolden, dropPlatformCommands(collectCliHelpFor(t, restHelpRoots)))
}

// TestZhRestOutputsAreUnchanged pins the zh-CN text these commands print that is
// not part of --help. The cases call the code that renders the text, so a change
// to what an existing zh-CN deployment prints turns this red.
func TestZhRestOutputsAreUnchanged(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.ZhCN))
	compareGolden(t, restOutputsGolden, collectRestOutputs(t))
}

// collectRestOutputs renders the representative outputs of the remaining groups,
// one "=== <name>" block each, in a fixed order.
func collectRestOutputs(t *testing.T) string {
	t.Helper()
	var cases []setupCase

	// steward status: the three state labels and the "steward is off" hint.
	for _, state := range []string{"running", "idle", "not_started"} {
		cases = append(cases, setupCase{name: "steward/state/" + state, text: stewardStateLabel(state)})
	}

	// repo init / migrate: the project_key notes when no gofer config is readable
	// and when nothing matches.
	cases = append(cases, setupCase{name: "repo/project-key/no-config",
		text: strings.Join(trackerProjectKeyNotes(nil, "/repo", ""), "\n")})

	// memory candidates: one list row carrying the inherited scope.
	cases = append(cases, setupCase{name: "memory/candidate/row", text: memoryCandidateLine(jobstore.MemoryCandidate{
		ID: 7, Status: "pending", ProjectKey: "p1", JobID: "j1", CreatedAt: 1_700_000_000,
		Text: "prefer the repository accessor", WhenPaths: []string{"sub/a/**", "sub/b/**"}, MemoryKey: "k-1",
	}, time.Unix(1_700_000_000, 0).Add(48*time.Hour))})

	// memory scope: the apply summary, the threshold note and the unknown keys.
	cmd := bindCmd(NewMemoryCmd())
	cases = append(cases, setupCase{name: "memory/scope/apply", text: strings.TrimRight(captureOutput(t, func() {
		printMemoryScopeApplyResult(cmd, memoryScopeOutput{
			Applied: []string{"k-1", "k-2"}, AlreadyScoped: 1, Unresolved: 2,
			BelowThreshold: []string{"k-3"}, Unknown: []string{"k-9"},
		})
	}), "\n")})

	// dual mode: a command that only went remote because this box has no local
	// server config explains the detour.
	cases = append(cases, setupCase{name: "dual-mode/fallback",
		text: errText(serverAPIChoice{remote: true, fallback: true}.wrap(errors.New("dial tcp 127.0.0.1:1: connect: connection refused")))})

	var sb strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&sb, "=== %s\n%s\n", c.name, c.text)
	}
	return sb.String()
}

// errText is an error's message ("" for nil), so a case can pin it.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestRestCommandGroupsAreCovered: every top-level command belongs to exactly one
// of the three help goldens, so a command added later cannot skip the CLI
// catalog silently.
func TestRestCommandGroupsAreCovered(t *testing.T) {
	covered := map[string]bool{}
	for _, roots := range [][]string{migratedHelpRoots, setupHelpRoots, restHelpRoots} {
		for _, root := range roots {
			covered[root] = true
		}
	}
	for name := range NewApp("test").Commands() {
		if !covered[strings.TrimSpace(name)] {
			t.Errorf("command %q is in no CLI help golden: add it to restHelpRoots", name)
		}
	}
}
