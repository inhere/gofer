package workflowstep

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs git in dir (test helper; the push step is the only thing that needs a repo).
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestNamesAndLookup(t *testing.T) {
	want := []string{NamePush, NameWaitCI, NameWaitAssets}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
		st, ok := Lookup(want[i])
		if !ok || st.Name != want[i] || st.Runner != RunnerExec || st.Decode == nil || st.Validate == nil || st.Run == nil {
			t.Fatalf("Lookup(%q) = %+v ok=%v", want[i], st, ok)
		}
		if st.MinWorkerProtocol == 0 {
			t.Errorf("%s has no worker protocol floor", st.Name)
		}
	}
}

func TestParseUses(t *testing.T) {
	if name, err := ParseUses("gofer/push"); err != nil || name != NamePush {
		t.Fatalf("ParseUses(gofer/push) = %q %v", name, err)
	}
	for _, bad := range []string{"", "push", "actions/checkout@v4", "gofer/nope", "gofer/"} {
		if _, err := ParseUses(bad); err == nil {
			t.Errorf("ParseUses(%q) should be refused", bad)
		}
	}
	if !strings.Contains(errText(t, "gofer/nope"), "unknown built-in step") {
		t.Fatalf("the refusal should name the unknown step")
	}
}

func errText(t *testing.T, uses string) string {
	t.Helper()
	_, err := ParseUses(uses)
	if err == nil {
		t.Fatalf("ParseUses(%q) should fail", uses)
	}
	return err.Error()
}

// Unknown keys in `with:` are refused: a misspelled argument must not be silently
// dropped (a release workflow that pushes nothing is worse than a submit error).
func TestValidateStrictWith(t *testing.T) {
	cases := []struct {
		name string
		uses string
		with map[string]any
		ok   bool
	}{
		{"push ok", "gofer/push", map[string]any{"refs": []any{"main", "v1"}}, true},
		{"push unknown key", "gofer/push", map[string]any{"refs": []any{"main"}, "ref": "main"}, false},
		{"push no refs", "gofer/push", map[string]any{}, false},
		{"push empty ref", "gofer/push", map[string]any{"refs": []any{""}}, false},
		{"wait-ci ok", "gofer/wait-ci", map[string]any{"ref": "v1", "required": []any{"Unit-Tests"}, "code_scanning": true, "timeout_min": 40}, true},
		{"wait-ci no ref", "gofer/wait-ci", map[string]any{"required": []any{"x"}}, false},
		{"wait-ci bad key", "gofer/wait-ci", map[string]any{"ref": "v1", "timeout": 40}, false},
		{"wait-ci negative timeout", "gofer/wait-ci", map[string]any{"ref": "v1", "timeout_min": -1}, false},
		{"wait-assets ok", "gofer/wait-assets", map[string]any{"tag": "v1", "assets": []any{"*.zip"}}, true},
		{"wait-assets no assets", "gofer/wait-assets", map[string]any{"tag": "v1"}, false},
		{"wait-assets unknown key", "gofer/wait-assets", map[string]any{"tag": "v1", "assets": []any{"a"}, "glob": "b"}, false},
	}
	for _, c := range cases {
		err := Validate(c.uses, c.with)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: should be refused", c.name)
		}
	}
}

// Run executes a step by name after re-validating its `with:` block, and push really
// pushes to a local bare remote.
func TestRunPush(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	git(t, dir, "init", "--bare", "-q", bare)
	git(t, dir, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "one", "--no-gpg-sign")
	git(t, work, "tag", "v1.0.0")
	git(t, work, "remote", "add", "origin", bare)

	res, err := Run(context.Background(), NamePush, map[string]any{"refs": []any{"main", "v1.0.0"}}, work)
	if err != nil {
		t.Fatalf("Run push: %v", err)
	}
	out, _ := res.(map[string]any)
	if out["ok"] != true {
		t.Fatalf("result = %+v", res)
	}
	sha := git(t, work, "rev-parse", "refs/heads/main")
	if got := git(t, bare, "rev-parse", "refs/heads/main"); got != sha {
		t.Errorf("remote main = %s, want %s", got, sha)
	}

	if _, err := Run(context.Background(), NamePush, map[string]any{"refs": []any{"nothing"}}, work); err == nil {
		t.Fatal("an unknown ref must fail the step")
	}
	if _, err := Run(context.Background(), "nope", nil, work); err == nil {
		t.Fatal("an unknown step must fail")
	}
}
