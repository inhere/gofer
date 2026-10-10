package tracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedBlockSkipsClaudeImportingAgents: a CLAUDE.md whose content is
// `@AGENTS.md` already loads AGENTS.md, so the gofer block goes into AGENTS.md
// only (Claude would otherwise read it twice). Migration still strips a bd
// block from such a CLAUDE.md, without putting a gofer block in its place.
func TestManagedBlockSkipsClaudeImportingAgents(t *testing.T) {
	write := func(root, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(root, name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	root := t.TempDir()
	write(root, "AGENTS.md", "# agents\n")
	write(root, "CLAUDE.md", "@AGENTS.md\n\nlocal notes\n")
	if _, _, err := Init(root, "demo", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(root, "AGENTS.md"), beginBlock) {
		t.Fatal("init: AGENTS.md should carry the gofer block")
	}
	if strings.Contains(read(root, "CLAUDE.md"), beginBlock) {
		t.Fatal("init: CLAUDE.md imports AGENTS.md and must not repeat the block")
	}

}

// TestInitRefreshesStaleManagedBlock: re-running init replaces an older gofer block
// in place and keeps the text around it.
func TestInitRefreshesStaleManagedBlock(t *testing.T) {
	root := t.TempDir()
	stale := "# head\n\n" + beginBlock + "\nold rules\n" + endBlock + "\n\n## tail\n"
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := Init(root, "demo", false); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# head\n\n" + ManagedBlock() + "\n## tail\n"
	if string(b) != want {
		t.Fatalf("refreshed AGENTS.md:\n%s\nwant:\n%s", b, want)
	}
	if !strings.Contains(string(b), "gofer repo status --changed") {
		t.Fatal("block should point at repo status --changed")
	}
}

// TestManagedBlockFollowsCommitPolicy: the managed block's commit line is the
// prime's CommitPolicyText for every policy, a new tracker defaults to
// local-commit, and re-running init with another policy rewrites both the config
// and the block in place.
func TestManagedBlockFollowsCommitPolicy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	s, _, err := Init(root, "demo", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := s.ReadConfig()
	if err != nil || cfg.CommitPolicy != DefaultCommitPolicy {
		t.Fatalf("new tracker commit_policy = %q, %v", cfg.CommitPolicy, err)
	}
	for _, policy := range []string{"ask", "none", "local-commit"} {
		if _, _, err := InitWith(root, InitOptions{Prefix: "demo", CommitPolicy: policy}); err != nil {
			t.Fatalf("%s: %v", policy, err)
		}
		text, _ := CommitPolicyText(policy)
		block := read()
		if strings.Count(block, beginBlock) != 1 || !strings.Contains(block, "- "+text+"\n") {
			t.Fatalf("%s: block does not carry the policy text %q:\n%s", policy, text, block)
		}
		for _, other := range []string{"ask", "none", "local-commit"} {
			if other == policy {
				continue
			}
			if otherText, _ := CommitPolicyText(other); strings.Contains(block, otherText) {
				t.Fatalf("%s: block still carries the %s text", policy, other)
			}
		}
		prime, err := s.Prime()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prime, "## 提交策略\n"+text+"\n") {
			t.Fatalf("%s: prime header disagrees with the block:\n%s", policy, prime)
		}
		if cfg, _ := s.ReadConfig(); cfg.CommitPolicy != policy {
			t.Fatalf("config commit_policy = %q, want %q", cfg.CommitPolicy, policy)
		}
	}
	// A plain re-run keeps the configured policy.
	if _, _, err := InitWith(root, InitOptions{CommitPolicy: "ask"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Init(root, "demo", false); err != nil {
		t.Fatal(err)
	}
	if ask, _ := CommitPolicyText("ask"); !strings.Contains(read(), ask) {
		t.Fatal("re-running init without --commit-policy reset the block")
	}
	if _, _, err := InitWith(root, InitOptions{CommitPolicy: "sometimes"}); err == nil || !strings.Contains(err.Error(), "local-commit, ask or none") {
		t.Fatalf("unknown policy accepted: %v", err)
	}
}
