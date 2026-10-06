package jobstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func mustSession(t *testing.T, s *Store, sid string) AgentSession {
	t.Helper()
	a, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p1", Cwd: "/ws/a"})
	assert.NoErr(t, err)
	return a
}

func TestWorkItemCreateUpdateAndRevConflict(t *testing.T) {
	s := openTest(t)
	w, err := s.CreateWorkItem(WorkItemInput{Title: "  修复登录  ", ProjectKey: "p1", By: "human:a"})
	assert.NoErr(t, err)
	assert.Eq(t, "修复登录", w.Title)
	assert.Eq(t, WorkActive, w.Status)
	assert.Eq(t, int64(1), w.Rev)
	assert.Eq(t, WorkSourceAuto, w.StatusSource)

	_, err = s.CreateWorkItem(WorkItemInput{Title: "  "})
	assert.True(t, errors.Is(err, ErrWorkInvalid))

	st, goal := WorkNeedsOnsite, "到现场换设备"
	got, summary, err := s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &st, Goal: &goal}, 1, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), got.Rev)
	assert.Eq(t, WorkSourceHuman, got.StatusSource)
	assert.True(t, strings.Contains(summary, "状态：active → needs_onsite"))
	assert.True(t, strings.Contains(summary, "目标已更新"))

	// Stale rev -> conflict, nothing written.
	title := "别的标题"
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Title: &title}, 1, "human:b")
	assert.True(t, errors.Is(err, ErrWorkItemConflict))
	cur, _, _ := s.GetWorkItem(w.ID)
	assert.Eq(t, "修复登录", cur.Title)

	// No-op patch writes nothing and keeps rev.
	same := "修复登录"
	got, summary, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Title: &same}, 0, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, "", summary)
	assert.Eq(t, int64(2), got.Rev)

	// Every field change is journaled as a status line.
	j, err := s.ListWorkJournal(w.ID, 0, 0)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(j)) // create + the one real update
	assert.Eq(t, WorkJournalStatus, j[1].Kind)

	// done sets closed_at, reopening clears it.
	done := WorkDone
	got, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &done}, 0, "human:a")
	assert.NoErr(t, err)
	assert.True(t, got.ClosedAt > 0)
	act := WorkActive
	got, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &act}, 0, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), got.ClosedAt)

	bad := "nope"
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &bad}, 0, "x")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, _, err = s.UpdateWorkItem("w-missing", WorkItemPatch{Title: &title}, 0, "x")
	assert.True(t, errors.Is(err, ErrWorkItemNotFound))
}

func TestEnsureDraftWorkItemForSessionIsIdempotent(t *testing.T) {
	s := openTest(t)
	a := mustSession(t, s, "sess-1111-2222")
	w, created, err := s.EnsureDraftWorkItemForSession(a, "\n  帮我看看登录为什么失败\n第二行")
	assert.NoErr(t, err)
	assert.True(t, created)
	assert.Eq(t, "帮我看看登录为什么失败", w.Title)
	assert.Eq(t, WorkOriginAuto, w.Source)
	assert.True(t, w.Unsorted)
	assert.Eq(t, "p1", w.ProjectKey)
	assert.Eq(t, "/ws/a", w.Workspace)

	_, created, err = s.EnsureDraftWorkItemForSession(a, "再问一个问题")
	assert.NoErr(t, err)
	assert.False(t, created)
	items, err := s.ListWorkItems(WorkListOpts{})
	assert.NoErr(t, err)
	assert.Eq(t, 1, len(items))

	// A session that was split off / merged away from its draft never gets a new one.
	assert.NoErr(t, s.DetachWorkSession(w.ID, "sess-1111-2222", "x"))
	_, created, _ = s.EnsureDraftWorkItemForSession(a, "again")
	assert.False(t, created)

	// Empty prompt -> nothing.
	b := mustSession(t, s, "sess-b")
	_, created, err = s.EnsureDraftWorkItemForSession(b, "  \n ")
	assert.NoErr(t, err)
	assert.False(t, created)
}

func TestWorkItemListFilters(t *testing.T) {
	s := openTest(t)
	a, _ := s.CreateWorkItem(WorkItemInput{Title: "alpha", ProjectKey: "p1", Workspace: "/w1", Unsorted: true, Source: WorkOriginAuto})
	b, _ := s.CreateWorkItem(WorkItemInput{Title: "beta", ProjectKey: "p2", Workspace: "/w2", SessionIDs: []string{"s-x"}})
	c, _ := s.CreateWorkItem(WorkItemInput{Title: "gamma done", Status: WorkDone})

	all, _ := s.ListWorkItems(WorkListOpts{})
	assert.Eq(t, 2, len(all)) // done hidden
	all, _ = s.ListWorkItems(WorkListOpts{IncludeClosed: true})
	assert.Eq(t, 3, len(all))
	got, _ := s.ListWorkItems(WorkListOpts{Project: "p2"})
	assert.Eq(t, 1, len(got))
	assert.Eq(t, b.ID, got[0].ID)
	yes := true
	got, _ = s.ListWorkItems(WorkListOpts{Unsorted: &yes})
	assert.Eq(t, 1, len(got))
	assert.Eq(t, a.ID, got[0].ID)
	got, _ = s.ListWorkItems(WorkListOpts{Workspace: "/w2"})
	assert.Eq(t, b.ID, got[0].ID)
	got, _ = s.ListWorkItems(WorkListOpts{SessionID: "s-x"})
	assert.Eq(t, 1, len(got))
	got, _ = s.ListWorkItems(WorkListOpts{Statuses: []string{WorkDone}})
	assert.Eq(t, c.ID, got[0].ID)
	got, _ = s.ListWorkItems(WorkListOpts{Query: "alp"})
	assert.Eq(t, 1, len(got))
	_, err := s.ListWorkItems(WorkListOpts{Statuses: []string{"zzz"}})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
}

func TestMergeWorkItemsMovesSessionsJournalAndLinks(t *testing.T) {
	s := openTest(t)
	target, _ := s.CreateWorkItem(WorkItemInput{Title: "target", SessionIDs: []string{"s1"}})
	src1, _ := s.CreateWorkItem(WorkItemInput{Title: "src1", SessionIDs: []string{"s2"}})
	src2, _ := s.CreateWorkItem(WorkItemInput{Title: "src2", SessionIDs: []string{"s1", "s3"}})
	_, err := s.AppendWorkJournal(src1.ID, WorkJournalNote, "note from src1", "human:a")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(src1.ID, WorkLinkIssue, "ISS-1", "human:a")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(target.ID, WorkLinkIssue, "ISS-1", "human:a")
	assert.NoErr(t, err)
	assert.NoErr(t, s.DetachWorkSession(src2.ID, "s3", "x")) // s3 past on src2

	got, err := s.MergeWorkItems(target.ID, []string{src1.ID, src2.ID, src1.ID}, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, target.ID, got.ID)

	sess, _ := s.ListWorkItemSessions(target.ID)
	roles := map[string]string{}
	for _, r := range sess {
		roles[r.SessionID] = r.Role
	}
	assert.Eq(t, map[string]string{"s1": "current", "s2": "current", "s3": "past"}, roles)
	links, _ := s.ListWorkLinks(target.ID)
	assert.Eq(t, 1, len(links)) // deduped

	j, _ := s.ListWorkJournal(target.ID, 0, 0)
	var sawNote, sawMerge bool
	for _, e := range j {
		if e.Text == "note from src1" && e.OriginItem == src1.ID {
			sawNote = true
		}
		if strings.HasPrefix(e.Text, "合并了 ") {
			sawMerge = true
		}
	}
	assert.True(t, sawNote)
	assert.True(t, sawMerge)

	// Sources vanish from the default list but stay resolvable.
	items, _ := s.ListWorkItems(WorkListOpts{})
	assert.Eq(t, 1, len(items))
	srcGot, ok, _ := s.GetWorkItem(src1.ID)
	assert.True(t, ok)
	assert.Eq(t, target.ID, srcGot.MergedInto)
	assert.Eq(t, WorkDropped, srcGot.Status)
	srcSess, _ := s.ListWorkItemSessions(src1.ID)
	assert.Eq(t, 0, len(srcSess))

	// Guards.
	_, err = s.MergeWorkItems(target.ID, []string{target.ID}, "x")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.MergeWorkItems(target.ID, []string{src1.ID}, "x")
	assert.True(t, errors.Is(err, ErrWorkInvalid)) // already merged
	_, err = s.MergeWorkItems(target.ID, []string{"w-nope"}, "x")
	assert.True(t, errors.Is(err, ErrWorkItemNotFound))
	_, err = s.MergeWorkItems(src1.ID, []string{target.ID}, "x")
	assert.True(t, errors.Is(err, ErrWorkInvalid)) // target was merged away
}

func TestSplitWorkItemMoveAndKeep(t *testing.T) {
	s := openTest(t)
	src, _ := s.CreateWorkItem(WorkItemInput{Title: "big", ProjectKey: "p1", Workspace: "/w", SessionIDs: []string{"s1", "s2"}})

	srcAfter, nw, err := s.SplitWorkItem(src.ID, SplitWorkItemInput{Title: "part two", SessionIDs: []string{"s2"}, By: "human:a"})
	assert.NoErr(t, err)
	assert.Eq(t, "p1", nw.ProjectKey)
	assert.Eq(t, "/w", nw.Workspace)
	assert.Eq(t, src.ID, srcAfter.ID)
	cur, _ := s.CurrentWorkSessionIDs(nil)
	assert.Eq(t, []string{"s1"}, cur[src.ID])
	assert.Eq(t, []string{"s2"}, cur[nw.ID])
	sessions, _ := s.ListWorkItemSessions(src.ID)
	assert.Eq(t, "past", sessions[len(sessions)-1].Role)

	// keep mode: the same session works for both.
	_, nw2, err := s.SplitWorkItem(src.ID, SplitWorkItemInput{Title: "part three", SessionIDs: []string{"s1"}, KeepSessions: true})
	assert.NoErr(t, err)
	cur, _ = s.CurrentWorkSessionIDs(nil)
	assert.Eq(t, []string{"s1"}, cur[src.ID])
	assert.Eq(t, []string{"s1"}, cur[nw2.ID])

	// Sessions must belong to the source; title required.
	_, _, err = s.SplitWorkItem(src.ID, SplitWorkItemInput{Title: "x", SessionIDs: []string{"nope"}})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, _, err = s.SplitWorkItem(src.ID, SplitWorkItemInput{Title: " "})
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	// Nothing leaked from the failed attempts.
	items, _ := s.ListWorkItems(WorkListOpts{})
	assert.Eq(t, 3, len(items))
}

func TestWorkRemindAndParkDueSweep(t *testing.T) {
	s := openTest(t)
	w, _ := s.CreateWorkItem(WorkItemInput{Title: "remind me"})
	p, _ := s.CreateWorkItem(WorkItemInput{Title: "parked"})
	now := s.unixNow()
	past, future := now-10, now+3600
	parked := WorkParked
	_, _, err := s.UpdateWorkItem(w.ID, WorkItemPatch{RemindAt: &past}, 0, "h")
	assert.NoErr(t, err)
	_, _, err = s.UpdateWorkItem(p.ID, WorkItemPatch{Status: &parked, ParkUntil: &past}, 0, "h")
	assert.NoErr(t, err)

	due, err := s.ListDueWorkItems(now)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(due))

	assert.NoErr(t, s.MarkWorkItemReminded(w.ID, past, "已发送到期提醒"))
	assert.NoErr(t, s.MarkWorkItemReminded(p.ID, past, "已发送到期提醒"))
	due, _ = s.ListDueWorkItems(now)
	assert.Eq(t, 0, len(due)) // announced once

	// A later remind_at re-arms it.
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{RemindAt: &future}, 0, "h")
	assert.NoErr(t, err)
	due, _ = s.ListDueWorkItems(now)
	assert.Eq(t, 0, len(due))
	due, _ = s.ListDueWorkItems(future + 1)
	assert.Eq(t, 1, len(due))

	// The "到期提醒" list view ignores reminded_at (until the human clears it).
	got, _ := s.ListWorkItems(WorkListOpts{Due: true, Now: now})
	assert.Eq(t, 1, len(got)) // parked one (remind for w moved to the future)
	assert.Eq(t, p.ID, got[0].ID)

	// done items never fire.
	done := WorkDone
	_, _, _ = s.UpdateWorkItem(p.ID, WorkItemPatch{Status: &done}, 0, "h")
	_, _, _ = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &done}, 0, "h")
	due, _ = s.ListDueWorkItems(future + 100)
	assert.Eq(t, 0, len(due))
}

func TestSetWorkItemAutoStatusRespectsHumanLock(t *testing.T) {
	s := openTest(t)
	w, _ := s.CreateWorkItem(WorkItemInput{Title: "auto"})
	changed, err := s.SetWorkItemAutoStatus(w.ID, WorkNeedsMe, "会话等待回复")
	assert.NoErr(t, err)
	assert.True(t, changed)
	changed, _ = s.SetWorkItemAutoStatus(w.ID, WorkNeedsMe, "again")
	assert.False(t, changed)

	onsite := WorkNeedsOnsite
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &onsite}, 0, "human:a")
	assert.NoErr(t, err)
	changed, _ = s.SetWorkItemAutoStatus(w.ID, WorkActive, "会话运行中")
	assert.False(t, changed) // human wins
	cur, _, _ := s.GetWorkItem(w.ID)
	assert.Eq(t, WorkNeedsOnsite, cur.Status)

	// Unlock back to auto, then the mapping applies again.
	auto := WorkSourceAuto
	_, _, err = s.UpdateWorkItem(w.ID, WorkItemPatch{StatusSource: &auto}, 0, "human:a")
	assert.NoErr(t, err)
	changed, _ = s.SetWorkItemAutoStatus(w.ID, WorkActive, "会话运行中")
	assert.True(t, changed)

	// Finished items are never auto-moved.
	done := WorkDone
	_, _, _ = s.UpdateWorkItem(w.ID, WorkItemPatch{Status: &done, StatusSource: &auto}, 0, "h")
	changed, _ = s.SetWorkItemAutoStatus(w.ID, WorkActive, "x")
	assert.False(t, changed)
}

func TestWorkLinksAndSessionsAreJournaled(t *testing.T) {
	s := openTest(t)
	w, _ := s.CreateWorkItem(WorkItemInput{Title: "t"})
	_, err := s.AddWorkLink(w.ID, WorkLinkJob, "job-1", "human:a")
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, WorkLinkJob, "job-1", "human:a") // idempotent, one line
	assert.NoErr(t, err)
	_, err = s.AddWorkLink(w.ID, "bogus", "x", "h")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	assert.NoErr(t, s.AttachWorkSession(w.ID, "sess-aaaaaaaaaa", "human:a"))
	assert.NoErr(t, s.AttachWorkSession(w.ID, "sess-aaaaaaaaaa", "human:a")) // idempotent
	ok, err := s.RemoveWorkLink(w.ID, WorkLinkJob, "job-1", "human:a")
	assert.NoErr(t, err)
	assert.True(t, ok)
	ok, _ = s.RemoveWorkLink(w.ID, WorkLinkJob, "job-1", "human:a")
	assert.False(t, ok)

	j, _ := s.ListWorkJournal(w.ID, 0, 0)
	n := 0
	for _, e := range j {
		if e.Kind == WorkJournalLink {
			n++
		}
	}
	assert.Eq(t, 3, n) // link, attach, unlink

	_, err = s.AppendWorkJournal(w.ID, "weird", "x", "h")
	assert.True(t, errors.Is(err, ErrWorkInvalid))
	_, err = s.AppendWorkJournal("w-nope", WorkJournalNote, "x", "h")
	assert.True(t, errors.Is(err, ErrWorkItemNotFound))
}

func TestWorkChangeEventAndKV(t *testing.T) {
	s := openTest(t)
	var kinds []ChangeKind
	s.SetChangeHook(func(c Change) { kinds = append(kinds, c.Kind) })
	_, err := s.CreateWorkItem(WorkItemInput{Title: "ev"})
	assert.NoErr(t, err)
	assert.True(t, len(kinds) > 0)
	assert.Eq(t, ChangeWork, kinds[0])

	v, err := s.GetWorkKV("digest_last_date")
	assert.NoErr(t, err)
	assert.Eq(t, "", v)
	assert.NoErr(t, s.SetWorkKV("digest_last_date", "2026-10-05"))
	assert.NoErr(t, s.SetWorkKV("digest_last_date", "2026-10-06"))
	v, _ = s.GetWorkKV("digest_last_date")
	assert.Eq(t, "2026-10-06", v)
}
