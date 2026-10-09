package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

func x1Create(t *testing.T, root string, args ...string) string {
	t.Helper()
	out := trackerRunOK(t, root, append([]string{"issue", "create", "--json"}, args...)...)
	var item struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &item); err != nil || item.ID == "" {
		t.Fatalf("create JSON=%q err=%v", out, err)
	}
	return item.ID
}

func TestIssueCreatePositionalTitleAndBdStyleFlags(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init", "--prefix", "x1")
	out := trackerRunOK(t, root, "issue", "create", "positional title", "-p", "1", "-d", "desc", "-a", "ann", "-l", "proj01,backend", "--design", "dz", "--acceptance", "ac", "--type", "bug", "--json")
	var item struct {
		Title, Type, Description, Assignee, Design string
		AcceptanceCriteria                         string `json:"acceptance_criteria"`
		Priority                                   int
		Tags                                       []string
	}
	if err := json.Unmarshal([]byte(out), &item); err != nil {
		t.Fatal(err)
	}
	if item.Title != "positional title" || item.Priority != 1 || item.Description != "desc" || item.Assignee != "ann" || item.Design != "dz" || item.AcceptanceCriteria != "ac" || item.Type != "bug" || strings.Join(item.Tags, ",") != "proj01,backend" {
		t.Fatalf("create: %s", out)
	}
	if out, code := trackerCLI(t, root, "issue", "create"); code == 0 || !strings.Contains(out, "title") {
		t.Fatalf("missing title must fail: %d %s", code, out)
	}
}

func TestIssueUpdateFieldsClearAndFilters(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init", "--prefix", "x1")
	parent := x1Create(t, root, "-t", "parent", "-p", "3")
	child := x1Create(t, root, "-t", "child", "-p", "2", "-l", "proj01")
	other := x1Create(t, root, "-t", "other", "-p", "0", "-l", "proj02", "-a", "bob")
	out := trackerRunOK(t, root, "issue", "update", child, "-p", "1", "-d", "new desc", "--design", "dsg", "--acceptance", "acc", "-a", "ann", "--owner", "own", "--type", "feature", "--parent", parent, "-l", "extra", "--json")
	for _, want := range []string{`"priority":1`, `"description":"new desc"`, `"design":"dsg"`, `"acceptance_criteria":"acc"`, `"assignee":"ann"`, `"owner":"own"`, `"type":"feature"`, `"parent":"` + parent + `"`, `"extra"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("update missing %s: %s", want, out)
		}
	}
	// Priority 0 through the CLI is a real update, not "unset".
	if out := trackerRunOK(t, root, "issue", "update", child, "-p", "0", "--json"); !strings.Contains(out, `"priority":0`) {
		t.Fatalf("priority 0: %s", out)
	}
	out = trackerRunOK(t, root, "issue", "update", child, "--clear", "design,assignee,parent", "--json")
	if strings.Contains(out, `"design"`) || strings.Contains(out, `"assignee"`) || strings.Contains(out, `"parent"`) || !strings.Contains(out, `"description":"new desc"`) {
		t.Fatalf("clear: %s", out)
	}
	if out, code := trackerCLI(t, root, "issue", "update", child, "--clear", "title"); code == 0 || !strings.Contains(out, "cannot clear") {
		t.Fatalf("--clear title must fail: %d %s", code, out)
	}
	// ls filters: label alias per sub-project, assignee, priority, sort.
	if out := trackerRunOK(t, root, "issue", "ls", "-l", "proj01", "--json"); !strings.Contains(out, child) || strings.Contains(out, other) {
		t.Fatalf("ls -l proj01: %s", out)
	}
	if out := trackerRunOK(t, root, "issue", "ls", "--assignee", "bob", "--json"); !strings.Contains(out, other) || strings.Contains(out, child) {
		t.Fatalf("ls --assignee: %s", out)
	}
	if out := trackerRunOK(t, root, "issue", "ls", "--priority", "0", "--json"); !strings.Contains(out, other) || !strings.Contains(out, child) || strings.Contains(out, parent) {
		t.Fatalf("ls --priority 0: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(trackerRunOK(t, root, "issue", "ls", "--sort", "priority", "-n", "2")), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], other) && !strings.HasPrefix(lines[0], child) {
		t.Fatalf("sort/limit: %q", lines)
	}
}

func TestIssueCommentReopenDepAndShow(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init", "--prefix", "x1")
	t.Setenv("GOFER_CALLER", "tester")
	a, b := x1Create(t, root, "-t", "A"), x1Create(t, root, "-t", "B", "--parent", "")
	trackerRunOK(t, root, "issue", "update", b, "--parent", a)
	trackerRunOK(t, root, "issue", "dep", "add", b, a)
	trackerRunOK(t, root, "issue", "comment", a, "first", "comment", "text")
	trackerRunOK(t, root, "issue", "update", a, "--append-notes", "a note")

	show := trackerRunOK(t, root, "issue", "show", a)
	for _, want := range []string{"children: " + b, "blocks: " + b, "first comment text", "(tester)", "a note"} {
		if !strings.Contains(show, want) {
			t.Fatalf("show %s missing %q:\n%s", a, want, show)
		}
	}
	showB := trackerRunOK(t, root, "issue", "show", b)
	if !strings.Contains(showB, "parent: "+a) || !strings.Contains(showB, "blocked-by: "+a) || !strings.Contains(showB, "depends-on: "+a+" (blocks)") {
		t.Fatalf("show B:\n%s", showB)
	}
	if out := trackerRunOK(t, root, "issue", "show", a, b, "--json"); !strings.HasPrefix(out, "[") {
		t.Fatalf("multi-id show --json must be an array: %s", out)
	}
	depLs := trackerRunOK(t, root, "issue", "dep", "ls", a)
	if !strings.Contains(depLs, "blocks: "+b) {
		t.Fatalf("dep ls: %s", depLs)
	}
	trackerRunOK(t, root, "issue", "dep", "rm", b, a)
	if out := trackerRunOK(t, root, "issue", "ready", "--json"); !strings.Contains(out, b) {
		t.Fatalf("removed dependency must unblock: %s", out)
	}
	if out, code := trackerCLI(t, root, "issue", "dep", "rm", b, a); code == 0 {
		t.Fatalf("second rm must fail: %s", out)
	}
	trackerRunOK(t, root, "issue", "dep", "add", b, a, "--type", "related")
	trackerRunOK(t, root, "issue", "close", a, "--reason", "done")
	reopened := trackerRunOK(t, root, "issue", "reopen", a, "--reason", "regressed", "--json")
	if !strings.Contains(reopened, `"status":"open"`) || strings.Contains(reopened, "close_reason") || strings.Contains(reopened, "closed_at") || !strings.Contains(reopened, "reopened: regressed") {
		t.Fatalf("reopen: %s", reopened)
	}
	if out, code := trackerCLI(t, root, "issue", "reopen", a); code == 0 {
		t.Fatalf("reopening an open issue must fail: %s", out)
	}
}

func TestMemoryShowMultipleRecallAndKeywordSearch(t *testing.T) {
	root := t.TempDir()
	trackerRunOK(t, root, "repo", "init", "--prefix", "x1")
	trackerRunOK(t, root, "memory", "set", "k-one", "alpha text")
	trackerRunOK(t, root, "memory", "set", "k-two", "beta text")
	out := trackerRunOK(t, root, "memory", "show", "k-one", "k-two")
	if !strings.Contains(out, "key: k-one") || !strings.Contains(out, "---\nalpha text") || !strings.Contains(out, "key: k-two") || !strings.Contains(out, "---\nbeta text") {
		t.Fatalf("multi show: %s", out)
	}
	if out := trackerRunOK(t, root, "memory", "recall", "k-one", "--json"); !strings.HasPrefix(out, "{") {
		t.Fatalf("single key keeps object shape: %s", out)
	}
	if out := trackerRunOK(t, root, "memory", "show", "k-one", "k-two", "--json"); !strings.HasPrefix(out, "[") {
		t.Fatalf("several keys give an array: %s", out)
	}
	out, code := trackerCLI(t, root, "memory", "show", "k-one", "nope")
	if code == 0 || !strings.Contains(out, "alpha text") || !strings.Contains(out, "nope") {
		t.Fatalf("missing key must still print the found ones and fail: %d %s", code, out)
	}
	if out := trackerRunOK(t, root, "memory", "ls", "BETA"); !strings.Contains(out, "k-two") || strings.Contains(out, "k-one") {
		t.Fatalf("keyword search is case-insensitive over content: %s", out)
	}
	if out := trackerRunOK(t, root, "memory", "memories", "k-one"); !strings.Contains(out, "k-one") {
		t.Fatalf("memories alias: %s", out)
	}
}
