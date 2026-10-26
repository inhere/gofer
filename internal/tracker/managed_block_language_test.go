package tracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// TestEnglishManagedBlockHasNoCJK: an English repository's block stands on its own —
// one leftover Chinese line would make the instructions an English agent reads
// mixed (G046). The commit line and the memory hint come from the prime catalog, so
// this also pins that they are the English ones.
func TestEnglishManagedBlockHasNoCJK(t *testing.T) {
	for _, policy := range []string{"local-commit", "ask", "none"} {
		block, err := ManagedBlockForIn(i18n.EN, policy)
		if err != nil {
			t.Fatal(err)
		}
		if hanPrime.MatchString(block) {
			t.Errorf("%s: English managed block carries CJK (%q):\n%s", policy, hanPrime.FindString(block), block)
		}
		for _, want := range []string{BeginBlock, EndBlock, "## Gofer Issue Tracker", "gofer issue ready", "gofer repo status --changed"} {
			if !strings.Contains(block, want) {
				t.Errorf("%s: block does not carry %q:\n%s", policy, want, block)
			}
		}
		commit, err := CommitPolicyTextIn(i18n.EN, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(block, "- "+commit+"\n") {
			t.Errorf("%s: block does not carry its English commit line %q:\n%s", policy, commit, block)
		}
		if !strings.Contains(block, "- "+memoryWriteHint(i18n.EN)+"\n") {
			t.Errorf("%s: block does not carry the English memory hint:\n%s", policy, block)
		}
	}
	if ManagedBlockIn(i18n.EN) != mustBlock(t, i18n.EN, DefaultCommitPolicy) {
		t.Error("ManagedBlockIn(en) is not the block of the default commit policy")
	}
}

// TestManagedBlockFollowsLanguageInPlace: the markers are language-independent, so a
// repository keeps exactly one block and a re-run in the other language rewrites it
// where it stands — an English block is found and replaced by a zh-CN run and the
// other way round, and the text around the block is never touched.
func TestManagedBlockFollowsLanguageInPlace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	head, tail := "# head\n\n", "\n## tail\n"
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	// A repository without a block gets one appended, in the language of the run.
	if _, _, err := InitWith(root, InitOptions{Prefix: "demo", Language: i18n.ZhCN}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != head+mustBlock(t, i18n.ZhCN, DefaultCommitPolicy) {
		t.Fatalf("AGENTS.md after the first init:\n%s", first)
	}
	if err := os.WriteFile(path, []byte(head+mustBlock(t, i18n.ZhCN, DefaultCommitPolicy)+tail), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhCN, "", i18n.EN} {
		if _, _, err := InitWith(root, InitOptions{Prefix: "demo", Language: lang}); err != nil {
			t.Fatalf("%q: %v", lang, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := head + mustBlock(t, lang, DefaultCommitPolicy) + tail
		if string(b) != want {
			t.Fatalf("%q: AGENTS.md after init:\n%s\nwant:\n%s", lang, b, want)
		}
	}
}

// TestManagedBlockLanguageSourceIsTheProject: the language comes from the project
// whose commit_policy the block describes, and an unknown one falls back to the
// deployment default rather than to the machine's locale.
func TestManagedBlockLanguageSourceIsTheProject(t *testing.T) {
	if got := i18n.Resolve(""); got != i18n.Default {
		t.Fatalf("an empty language must read as the default, got %q", got)
	}
	block, err := ManagedBlockForIn(i18n.Lang("zh_CN.UTF-8"), "ask")
	if err != nil {
		t.Fatal(err)
	}
	if block != mustBlock(t, i18n.ZhCN, "ask") {
		t.Fatal("an alias language must render like its canonical value")
	}
}

// mustBlock is ManagedBlockForIn for a test that cannot recover.
func mustBlock(t *testing.T, lang i18n.Lang, policy string) string {
	t.Helper()
	block, err := ManagedBlockForIn(lang, policy)
	if err != nil {
		t.Fatalf("%q/%s: %v", lang, policy, err)
	}
	return block
}
