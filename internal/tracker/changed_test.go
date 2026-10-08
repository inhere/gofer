package tracker

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	assert.Require(t, assert.NoErr(t, err, string(out)))
}

func changedFixture(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, _, err := Init(root, "t", true)
	assert.Require(t, assert.NoErr(t, err))
	assert.Require(t, assert.NoErr(t, s.WriteIssues([]Issue{
		{ID: "t-a", Title: "alpha", Status: "open", CreatedAt: "x"},
		{ID: "t-b", Title: "beta", Status: "open", CreatedAt: "x"},
		{ID: "t-c", Title: "gamma", Status: "open", CreatedAt: "x"},
		{ID: "t-d", Title: "delta", Status: "open", CreatedAt: "x"},
	})))
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{{Key: "k1", Content: "first\nsecond", UpdatedAt: "x"}, {Key: "k2", Content: "keep", UpdatedAt: "x"}}, nil
	})))
	gitIn(t, root, "init")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-m", "base")
	return s, root
}

func TestChangedSinceHEAD(t *testing.T) {
	s, _ := changedFixture(t)

	rep, err := s.ChangedSinceHEAD()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, []string{"no tracker changes vs HEAD"}, rep.Lines())
	assert.True(t, rep.InGit)

	assert.Require(t, assert.NoErr(t, s.WriteIssues([]Issue{
		{ID: "t-a", Title: "alpha", Status: "in_progress", CreatedAt: "x"},                        // status change
		{ID: "t-b", Title: "beta", Status: "open", Priority: 2, CreatedAt: "x", Description: "d"}, // other fields
		{ID: "t-c", Title: "gamma", Status: "open", CreatedAt: "x"},                               // unchanged
		{ID: "t-e", Title: "epsilon", Status: "open", CreatedAt: "x"},                             // added; t-d removed
	})))
	long := strings.Repeat("长", 70)
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func([]Memory) ([]Memory, error) {
		return []Memory{{Key: "k1", Content: "first\nsecond", UpdatedAt: "x"}, {Key: "k3", Content: long}}, nil
	})))

	rep, err = s.ChangedSinceHEAD()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, []string{
		"~ t-a open→in_progress alpha",
		"~ t-b open beta (fields: description,priority)",
		"+ t-e open epsilon",
		"- t-d delta",
		"+ k3 " + strings.Repeat("长", 60) + "…",
		"- k2 keep",
		"2 added, 2 changed, 2 removed",
	}, rep.Lines())
}

func TestChangedSinceHEADMemoryChangedAndJSONFields(t *testing.T) {
	s, _ := changedFixture(t)
	assert.Require(t, assert.NoErr(t, s.UpdateMemories(func(items []Memory) ([]Memory, error) {
		items[1].Content = "new content\nmore"
		return items, nil
	})))
	rep, err := s.ChangedSinceHEAD()
	assert.Require(t, assert.NoErr(t, err))
	assert.Eq(t, []string{"~ k2 new content (fields: content)", "0 added, 1 changed, 0 removed"}, rep.Lines())
	assert.Eq(t, "memory", rep.Changes[0].Kind)
	assert.Eq(t, []string{"content"}, rep.Changes[0].Fields)
}

func TestChangedSinceHEADNotInGit(t *testing.T) {
	root := t.TempDir()
	s, _, err := Init(root, "t", true)
	assert.Require(t, assert.NoErr(t, err))
	_, err = s.CreateIssue(Issue{Title: "solo", Type: "task"})
	assert.Require(t, assert.NoErr(t, err))
	rep, err := s.ChangedSinceHEAD()
	assert.Require(t, assert.NoErr(t, err))
	assert.False(t, rep.InGit)
	assert.Eq(t, 1, rep.Added)
	assert.True(t, strings.HasPrefix(rep.Lines()[0], "+ t-"))
}

func TestChangedSinceHEADFileMissingAtHEAD(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init")
	gitIn(t, root, "commit", "--allow-empty", "-m", "empty")
	s, _, err := Init(root, "t", true)
	assert.Require(t, assert.NoErr(t, err))
	_, err = s.CreateIssue(Issue{Title: "fresh", Type: "task"})
	assert.Require(t, assert.NoErr(t, err))
	rep, err := s.ChangedSinceHEAD()
	assert.Require(t, assert.NoErr(t, err))
	assert.True(t, rep.InGit)
	assert.Eq(t, 1, rep.Added)
}
