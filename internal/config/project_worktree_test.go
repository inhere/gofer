package config

import (
	"strings"
	"testing"
)

// TestProjectWorktreeRenderDefaults proves an unconfigured project keeps the historical
// layout: tmp/gofer/wt/{job_id} on branch gofer/{job_id}, no links.
func TestProjectWorktreeRenderDefaults(t *testing.T) {
	var p ProjectConfig
	if got, want := p.RenderWorktreeDir("", "j-1"), "tmp/gofer/wt/j-1"; got != want {
		t.Fatalf("dir = %q, want %q", got, want)
	}
	if got, want := p.RenderWorktreeBranch("", "j-1"), "gofer/j-1"; got != want {
		t.Fatalf("branch = %q, want %q", got, want)
	}
	if links := p.WorktreeLinks(); len(links) != 0 {
		t.Fatalf("links = %v, want none", links)
	}
}

// TestProjectWorktreeSectionsDefaultIndependently proves a worktree section that sets
// only one of dir/branch leaves the OTHER at its own default (each field falls back to
// its own template, never to the other field's).
func TestProjectWorktreeSectionsDefaultIndependently(t *testing.T) {
	onlyDir := ProjectConfig{Worktree: &ProjectWorktree{Dir: ".worktrees/{name}"}}
	if got, want := onlyDir.RenderWorktreeBranch("t", "j-1"), "gofer/j-1"; got != want {
		t.Fatalf("branch with only dir set = %q, want %q", got, want)
	}
	onlyBranch := ProjectConfig{Worktree: &ProjectWorktree{Branch: "feat/{name}"}}
	if got, want := onlyBranch.RenderWorktreeDir("t", "j-1"), "tmp/gofer/wt/j-1"; got != want {
		t.Fatalf("dir with only branch set = %q, want %q", got, want)
	}
}

// TestProjectWorktreeRenderTemplates proves {name} / {job_id} expansion, with {name}
// falling back to the job id when no --worktree-name was given.
func TestProjectWorktreeRenderTemplates(t *testing.T) {
	p := ProjectConfig{Worktree: &ProjectWorktree{
		Dir:    ".worktrees/{name}",
		Branch: "feat/{name}",
		Links:  []string{"web/node_modules"},
	}}
	if got, want := p.RenderWorktreeDir("topic", "j-1"), ".worktrees/topic"; got != want {
		t.Fatalf("dir = %q, want %q", got, want)
	}
	if got, want := p.RenderWorktreeDir("", "j-1"), ".worktrees/j-1"; got != want {
		t.Fatalf("dir without name = %q, want %q", got, want)
	}
	if got, want := p.RenderWorktreeBranch("topic", "j-1"), "feat/topic"; got != want {
		t.Fatalf("branch = %q, want %q", got, want)
	}
	if got := p.RenderWorktreeDir("topic", "j-1"); strings.Contains(got, "{") {
		t.Fatalf("placeholder left in %q", got)
	}
}

// TestProjectWorktreeValidation covers what config validate must refuse: unknown
// placeholders, paths escaping the project, and link entries that are not project
// relative.
func TestProjectWorktreeValidation(t *testing.T) {
	ok := []*ProjectWorktree{
		nil,
		{Dir: ".worktrees/{name}", Branch: "feat/{job_id}"},
		{Links: []string{"web/node_modules", "vendor/x"}},
		{Dir: "tmp/wt/{name}"},
	}
	for i, w := range ok {
		p := ProjectConfig{Worktree: w}
		if err := p.validateWorktree("demo"); err != nil {
			t.Fatalf("case %d: unexpected error: %v", i, err)
		}
	}

	bad := []struct {
		name string
		w    *ProjectWorktree
		want string
	}{
		{"unknown placeholder", &ProjectWorktree{Dir: "wt/{branch}"}, "unknown placeholder"},
		{"escapes", &ProjectWorktree{Dir: "../outside"}, "must not escape"},
		{"absolute", &ProjectWorktree{Dir: "/abs/wt"}, "must be relative"},
		{"windows absolute", &ProjectWorktree{Dir: `C:\abs\wt`}, "must be relative"},
		{"link escapes", &ProjectWorktree{Links: []string{"../x"}}, "must not escape"},
		{"link placeholder", &ProjectWorktree{Links: []string{"{name}/deps"}}, "placeholder"},
		{"branch dotdot", &ProjectWorktree{Branch: "feat/../main"}, "worktree.branch"},
	}
	for _, c := range bad {
		p := ProjectConfig{Worktree: c.w}
		err := p.validateWorktree("demo")
		if err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error = %q, want it to mention %q", c.name, err, c.want)
		}
		if !strings.Contains(err.Error(), `project "demo"`) {
			t.Fatalf("%s: error = %q, want the project named", c.name, err)
		}
	}
}

// TestWorktreeNameValid covers the name charset that ends up in a path and a branch.
func TestWorktreeNameValid(t *testing.T) {
	for _, name := range []string{"", "topic", "a.b_c-1", "A1"} {
		if !WorktreeNameValid(name) {
			t.Fatalf("name %q should be valid", name)
		}
	}
	for _, name := range []string{".", "..", "../escape", "a/b", "a b", `a\b`, "a:b"} {
		if WorktreeNameValid(name) {
			t.Fatalf("name %q should be refused", name)
		}
	}
}
