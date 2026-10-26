package tracker

import (
	"bytes"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// The managed instruction block: the text `gofer repo init` keeps up to date in
// a repository's AGENTS.md / CLAUDE.md and `gofer repo status` reports on. It is
// read by an agent at the start of every session, so it is written in the
// repository's language (design 2026-10-27-i18n-design.md §3 / P1h).
//
// The markers stay language-independent: that is what lets a re-run find a block
// written in the other language and rewrite it in place, leaving the text around
// it untouched.
const beginBlock = "<!-- BEGIN GOFER TRACKER v:1 -->"
const endBlock = "<!-- END GOFER TRACKER -->"

// BeginBlock / EndBlock delimit the gofer-managed block in AGENTS.md / CLAUDE.md.
const (
	BeginBlock = beginBlock
	EndBlock   = endBlock
)

// managedBlockBody renders the text between the markers: the instructions
// template of lang, with its commit-policy line and memory hint taken from the
// same catalog `repo prime` reads, so block and prime always agree.
func managedBlockBody(lang i18n.Lang, policy string) (string, error) {
	commit, err := CommitPolicyTextIn(lang, policy)
	if err != nil {
		return "", err
	}
	return prompts.Render(lang, prompts.NameRepoBlock, prompts.RepoBlockData{
		CommitPolicy: commit,
		MemoryHint:   memoryWriteHint(lang),
	})
}

// ManagedBlockForIn renders the managed block for a commit_policy value in lang.
func ManagedBlockForIn(lang i18n.Lang, policy string) (string, error) {
	body, err := managedBlockBody(lang, policy)
	if err != nil {
		return "", err
	}
	return beginBlock + "\n" + body + "\n" + endBlock + "\n", nil
}

// ManagedBlockFor renders the managed block for a commit_policy value in the
// default language.
func ManagedBlockFor(policy string) (string, error) {
	return ManagedBlockForIn(i18n.Default, policy)
}

// ManagedBlockIn returns the managed block for the default commit policy in lang.
func ManagedBlockIn(lang i18n.Lang) string {
	block, _ := ManagedBlockForIn(lang, DefaultCommitPolicy)
	return block
}

// ManagedBlock returns the managed block for the default commit policy.
func ManagedBlock() string { return ManagedBlockIn(i18n.Default) }

// refreshManagedBlock replaces an existing gofer block with block, so re-running
// `repo init` brings older instructions (and a changed commit_policy or language)
// up to date. ok is false when the end marker is missing.
func refreshManagedBlock(b []byte, block string) ([]byte, bool) {
	start := bytes.Index(b, []byte(beginBlock))
	rel := bytes.Index(b[start:], []byte(endBlock))
	if rel < 0 {
		return nil, false
	}
	end := start + rel + len(endBlock)
	if end < len(b) && b[end] == '\n' {
		end++
	}
	out := append([]byte(nil), b[:start]...)
	out = append(out, block...)
	return append(out, b[end:]...), true
}
