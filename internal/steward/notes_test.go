package steward

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestNotesVersionConflictAndHistory(t *testing.T) {
	e := newEnv(t)
	n, ok, err := e.svc.GetNotes(0)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 0, n.Version)

	v1, err := e.svc.SetNotes("# 笔记\n- 周三去现场\n", "human:me", 0)
	assert.NoErr(t, err)
	assert.Eq(t, 1, v1.Version)

	// A writer holding a stale version loses; nothing is overwritten.
	_, err = e.svc.SetNotes("别人的版本", "steward(claude-acp)", 0)
	assert.True(t, errors.Is(err, ErrNotesConflict))
	cur, _, _ := e.svc.GetNotes(0)
	assert.Eq(t, "# 笔记\n- 周三去现场\n", cur.Body)

	v2, err := e.svc.SetNotes("# 笔记\n- 周三去现场\n- 设备找老王\n", "steward(claude-acp)", 1)
	assert.NoErr(t, err)
	assert.Eq(t, 2, v2.Version)
	assert.Eq(t, "steward(claude-acp)", v2.By)

	old, ok, _ := e.svc.GetNotes(1)
	assert.True(t, ok)
	assert.Eq(t, "# 笔记\n- 周三去现场\n", old.Body)
	_, ok, _ = e.svc.GetNotes(9)
	assert.False(t, ok)
	hist, err := e.svc.NotesHistory()
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(hist))
	assert.Eq(t, 2, hist[0].Version) // newest first
}

// A slimmed rewrite is just another version: the long original stays readable.
func TestSlimmingNotesKeepsTheOldVersion(t *testing.T) {
	e := newEnv(t)
	long := strings.Repeat("- 一条很长的约定，写得啰嗦啰嗦啰嗦。\n", 300) // > 8KB
	_, err := e.svc.SetNotes(long, "human:me", 0)
	assert.NoErr(t, err)
	info, _ := e.svc.NotesStatus()
	assert.True(t, info.NeedSlim)
	assert.True(t, info.Bytes > 8<<10)
	assert.True(t, e.svc.Status().NotesNeedSlim)

	// The next review asks for the slim-down.
	prompt := reviewPrompt(TriggerDaily, "2026-10-06", nil, 0, nil, info.NeedSlim, e.svc.nowFn(), -1, nil)
	assert.True(t, strings.Contains(prompt, "笔记已超过 8KB"))

	short := "- 周三去现场\n"
	v2, err := e.svc.SetNotes(short, "steward(claude-acp)", 1)
	assert.NoErr(t, err)
	assert.Eq(t, 2, v2.Version)
	info, _ = e.svc.NotesStatus()
	assert.False(t, info.NeedSlim)
	assert.False(t, e.svc.Status().NotesNeedSlim)
	orig, ok, _ := e.svc.GetNotes(1)
	assert.True(t, ok)
	assert.Eq(t, long, orig.Body)
	hist, _ := e.svc.NotesHistory()
	assert.Eq(t, 2, len(hist))
}

func TestNotesTooLarge(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.SetNotes(strings.Repeat("x", 17<<10), "human:me", 0)
	assert.True(t, errors.Is(err, ErrNotesTooLarge))
	n, _, _ := e.svc.GetNotes(0)
	assert.Eq(t, 0, n.Version)
}
