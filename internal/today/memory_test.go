package today

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

func putMemory(t *testing.T, st *jobstore.Store, trackerID string, m map[string]any) {
	t.Helper()
	body, err := json.Marshal(m)
	assert.NoErr(t, err)
	key := m["key"].(string)
	updated, _ := m["updated_at"].(string)
	rev := int64(1)
	if rec, ok, _ := st.GetTrackerMemory(trackerID, key); ok {
		rev = rec.Rev + 1
	}
	assert.NoErr(t, st.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: trackerID, ID: key, Body: body, Rev: rev, UpdatedAt: updated}))
}

func daysAgo(d int) string {
	return testNow.Add(-time.Duration(d) * 24 * time.Hour).UTC().Format(time.RFC3339)
}

func seedMemories(t *testing.T, st *jobstore.Store) {
	t.Helper()
	assert.NoErr(t, st.UpsertTrackerRepo(jobstore.TrackerRepo{TrackerID: "trk", ProjectKey: "p1"}))
	putMemory(t, st, "trk", map[string]any{"key": "old-note", "content": "an old observation", "kind": "note", "updated_at": daysAgo(120)})
	putMemory(t, st, "trk", map[string]any{"key": "fresh", "content": "a fresh fact", "kind": "note", "updated_at": daysAgo(1)})
	putMemory(t, st, "trk", map[string]any{"key": "release-a", "content": "release steps: tag build push upgrade server", "kind": "note", "updated_at": daysAgo(2)})
	putMemory(t, st, "trk", map[string]any{"key": "release-b", "content": "release steps: tag build push upgrade server worker", "kind": "note", "updated_at": daysAgo(2)})
}

func memoryBody(t *testing.T, st *jobstore.Store, key string) (jobstore.TrackerRecord, tracker.Memory) {
	t.Helper()
	rec, ok, err := st.GetTrackerMemory("trk", key)
	assert.NoErr(t, err)
	assert.True(t, ok)
	var m tracker.Memory
	_ = json.Unmarshal(rec.Body, &m)
	return rec, m
}

func TestMemoryFindingsServerSide(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)

	got, err := svc.MemoryFindings(MemoryFindingsQuery{})
	assert.NoErr(t, err)
	byKey := map[string]MemoryFinding{}
	for _, f := range got {
		byKey[f.Key] = f
	}
	assert.Len(t, got, 3)
	assert.Eq(t, []string{MemoryActArchive}, byKey["old-note"].Actions)
	assert.Eq(t, "p1", byKey["old-note"].ProjectKey)
	assert.Eq(t, []string{MemoryActMerge}, byKey["release-a"].Actions)
	_, ok := byKey["fresh"]
	assert.False(t, ok)

	h, err := svc.MemoryHygiene()
	assert.NoErr(t, err)
	assert.Eq(t, 3, h.Findings)
	assert.Eq(t, MemorySuggestDailyCap, h.Remaining)
	assert.Contains(t, h.Signatures, "trk/old-note/archive")
}

func TestSuggestMemoryValidationCooldownAndCap(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)

	bad := []MemorySuggestInput{
		{TrackerID: "trk", Key: "old-note", Action: "delete", Reason: "x"},
		{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive},
		{TrackerID: "trk", Key: "old-note", Action: MemoryActMerge, Reason: "x"},
		{TrackerID: "trk", Key: "old-note", Action: MemoryActKind, Reason: "x", Payload: MemoryPayload{Kind: "note"}},
		{TrackerID: "trk", Key: "old-note", Action: MemoryActSummary, Reason: "x"},
		{TrackerID: "trk", Key: "old-note", Action: MemoryActWhen, Reason: "x"},
	}
	for _, in := range bad {
		_, _, err := svc.SuggestMemory(in, "steward(codex)", "")
		assert.True(t, errors.Is(err, ErrInvalidMemorySuggestion), in.Action)
	}
	_, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "nope", Action: MemoryActArchive, Reason: "x"}, "s", "")
	assert.True(t, errors.Is(err, ErrMemoryNotFound))

	sg, recorded, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive, Reason: "120 天未更新"}, "s", "j1")
	assert.NoErr(t, err)
	assert.True(t, recorded)
	again, recorded, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive, Reason: "再提一次"}, "s", "j1")
	assert.NoErr(t, err)
	assert.False(t, recorded)
	assert.Eq(t, sg.ID, again.ID)

	// Dismissed: not proposed again within the cooldown, and no longer an open finding.
	_, err = svc.DismissMemorySuggestion(sg.ID, "human:me")
	assert.NoErr(t, err)
	_, _, err = svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive, Reason: "x"}, "s", "")
	assert.True(t, errors.Is(err, ErrMemorySuggestCooldown))
	got, err := svc.MemoryFindings(MemoryFindingsQuery{TrackerID: "trk"})
	assert.NoErr(t, err)
	for _, f := range got {
		assert.NotEq(t, "old-note", f.Key)
	}
	_, err = svc.DismissMemorySuggestion(sg.ID, "human:me")
	assert.True(t, errors.Is(err, jobstore.ErrMemorySuggestionDecided))

	// Daily cap: 1 used, 4 more fit, the 6th is refused.
	for i, key := range []string{"fresh", "release-a", "release-b"} {
		_, rec, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: key, Action: MemoryActSummary, Reason: "补摘要",
			Payload: MemoryPayload{Summary: "摘要 " + key}}, "s", "")
		assert.NoErr(t, err, i)
		assert.True(t, rec)
	}
	_, _, err = svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "fresh", Action: MemoryActWhen, Reason: "常被查",
		Payload: MemoryPayload{Keywords: []string{"发版"}}}, "s", "")
	assert.NoErr(t, err)
	_, _, err = svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "fresh", Action: MemoryActKind, Reason: "长期约定",
		Payload: MemoryPayload{Kind: "rule"}}, "s", "")
	assert.True(t, errors.Is(err, ErrMemorySuggestCap))
}

func TestMemoryCardsAndAdopt(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)
	suggest := func(key, action string, p MemoryPayload) MemorySuggestion {
		t.Helper()
		sg, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: key, Action: action, Reason: "理由", Payload: p}, "s", "")
		assert.NoErr(t, err)
		return sg
	}
	arc := suggest("old-note", MemoryActArchive, MemoryPayload{})
	mrg := suggest("release-b", MemoryActMerge, MemoryPayload{Into: "release-a"})
	sum := suggest("fresh", MemoryActSummary, MemoryPayload{Summary: "一个新事实"})
	when := suggest("release-a", MemoryActWhen, MemoryPayload{Keywords: []string{"发版", "release"}})
	kind := suggest("fresh", MemoryActKind, MemoryPayload{Kind: "rule"})

	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	c, ok := find(cards, "memory:1")
	assert.True(t, ok)
	assert.Eq(t, KindMemory, c.Kind)
	assert.Eq(t, UrgencyNormal, c.Urgency)
	assert.Eq(t, "p1", c.ProjectKey)
	assert.Eq(t, arc.ID, c.Refs.MemorySuggestionID)
	assert.NotNil(t, c.Memory)
	assert.Eq(t, "note", c.Memory.CurrentKind)
	assert.Eq(t, []string{"adopt", "dismiss"}, []string{c.Actions[0].ID, c.Actions[1].ID})
	alive, err := svc.cardAlive("memory:1")
	assert.NoErr(t, err)
	assert.True(t, alive)

	// archive → an archive tombstone the clones turn into a local archive entry.
	_, err = svc.AdoptMemorySuggestion(arc.ID, "human:me")
	assert.NoErr(t, err)
	rec, _, _ := st.GetTrackerMemory("trk", "old-note")
	assert.True(t, rec.Deleted)
	assert.True(t, tracker.IsArchiveTombstone(rec.DeletedBy))
	assert.Contains(t, string(rec.Body), "archive_reason")
	alive, _ = svc.cardAlive("memory:1")
	assert.False(t, alive)

	// merge → target gets both bodies, source gets an ARCHIVE tombstone (clones keep it).
	_, err = svc.AdoptMemorySuggestion(mrg.ID, "human:me")
	assert.NoErr(t, err)
	_, target := memoryBody(t, st, "release-a")
	assert.Contains(t, target.Content, "upgrade server worker")
	rec, _, _ = st.GetTrackerMemory("trk", "release-b")
	assert.True(t, rec.Deleted)
	assert.True(t, tracker.IsArchiveTombstone(rec.DeletedBy))
	assert.Contains(t, string(rec.Body), `"merged_into":"release-a"`)

	_, err = svc.AdoptMemorySuggestion(sum.ID, "human:me")
	assert.NoErr(t, err)
	_, fresh := memoryBody(t, st, "fresh")
	assert.Eq(t, "一个新事实", fresh.Summary)

	// Each suggestion was made against the memory as it was then: release-a changed by
	// the merge, fresh by the summary — the other two are stale, not applied blindly.
	for _, sg := range []MemorySuggestion{when, kind} {
		_, err = svc.AdoptMemorySuggestion(sg.ID, "human:me")
		assert.True(t, errors.Is(err, ErrMemorySuggestionStale), sg.Action)
		assert.Contains(t, err.Error(), "记忆在建议之后被改过，请重新整理")
		row, _ := st.GetMemorySuggestion(sg.ID)
		assert.Eq(t, jobstore.MemorySuggestStale, row.State)
	}
	_, fresh = memoryBody(t, st, "fresh")
	assert.Eq(t, "note", fresh.EffectiveKind())
	_, rel := memoryBody(t, st, "release-a")
	assert.Nil(t, rel.When)

	_, err = svc.AdoptMemorySuggestion(kind.ID, "human:me")
	assert.True(t, errors.Is(err, jobstore.ErrMemorySuggestionDecided))
	cards, err = svc.Decisions(false)
	assert.NoErr(t, err)
	for _, c := range cards {
		assert.NotEq(t, KindMemory, c.Kind)
	}
}

// TestAdoptMemorySuggestionRevChecks: a sync that changed the memory or the merge target
// after the suggestion makes it stale; nothing is written.
func TestAdoptMemorySuggestionRevChecks(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)
	bump := func(key, content string) {
		t.Helper()
		rec, _, err := st.GetTrackerMemory("trk", key)
		assert.NoErr(t, err)
		var m map[string]any
		_ = json.Unmarshal(rec.Body, &m)
		m["content"] = content
		b, _ := json.Marshal(m)
		res, err := st.SyncTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk", ID: key, Body: b, UpdatedAt: "now"}, rec.Rev)
		assert.NoErr(t, err)
		assert.False(t, res.Conflict)
	}
	arc, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive, Reason: "旧"}, "s", "")
	assert.NoErr(t, err)
	bump("old-note", "刚被人改过的新内容")
	_, err = svc.AdoptMemorySuggestion(arc.ID, "human:me")
	assert.True(t, errors.Is(err, ErrMemorySuggestionStale))
	rec, _, _ := st.GetTrackerMemory("trk", "old-note")
	assert.False(t, rec.Deleted)
	// re-proposed against the new content, it applies
	arc, _, err = svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive, Reason: "仍然过时"}, "s", "")
	assert.NoErr(t, err)
	_, err = svc.AdoptMemorySuggestion(arc.ID, "human:me")
	assert.NoErr(t, err)
	rec, _, _ = st.GetTrackerMemory("trk", "old-note")
	assert.True(t, rec.Deleted)

	// merge: the TARGET changed after the suggestion.
	mrg, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "release-b", Action: MemoryActMerge, Reason: "重复",
		Payload: MemoryPayload{Into: "release-a"}}, "s", "")
	assert.NoErr(t, err)
	row, _ := st.GetMemorySuggestion(mrg.ID)
	assert.True(t, row.TargetRev > 0)
	bump("release-a", "目标被改写")
	_, err = svc.AdoptMemorySuggestion(mrg.ID, "human:me")
	assert.True(t, errors.Is(err, ErrMemorySuggestionStale))
	rec, _, _ = st.GetTrackerMemory("trk", "release-b")
	assert.False(t, rec.Deleted)
	_, target := memoryBody(t, st, "release-a")
	assert.Eq(t, "目标被改写", target.Content)
	row, _ = st.GetMemorySuggestion(mrg.ID)
	assert.Eq(t, jobstore.MemorySuggestStale, row.State)
}

// TestAdoptMergeIsIdempotent: an adoption that patched the target but did not finish (the
// tombstone or the decision write failed) can be adopted again without appending twice.
func TestAdoptMergeIsIdempotent(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)
	mrg, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "release-b", Action: MemoryActMerge, Reason: "重复",
		Payload: MemoryPayload{Into: "release-a"}}, "s", "")
	assert.NoErr(t, err)
	row, err := st.GetMemorySuggestion(mrg.ID)
	assert.NoErr(t, err)
	var p MemoryPayload
	_ = json.Unmarshal([]byte(row.PayloadJSON), &p)

	// A first attempt that died between its two writes: the target already holds the
	// merged text (patched exactly like the adoption does), the source is still live.
	_, src := memoryBody(t, st, "release-b")
	trec, target := memoryBody(t, st, "release-a")
	merged := strings.TrimRight(target.Content, "\n") + "\n\n" + strings.TrimSpace(src.Content)
	b, _ := json.Marshal(merged)
	_, err = st.PatchTrackerMemory("trk", "release-a", trec.Rev, map[string]json.RawMessage{"content": b}, "now", "human:me")
	assert.NoErr(t, err)

	_, err = svc.AdoptMemorySuggestion(mrg.ID, "human:me")
	assert.NoErr(t, err)
	_, target = memoryBody(t, st, "release-a")
	assert.Eq(t, merged, target.Content) // not appended a second time
	assert.Eq(t, 1, strings.Count(target.Content, strings.TrimSpace(src.Content)))
	rec, _, _ := st.GetTrackerMemory("trk", "release-b")
	assert.True(t, rec.Deleted)

	// The tombstone is recognised as this suggestion's own: applying again is a no-op.
	assert.NoErr(t, svc.applyMemorySuggestion(row, p, "human:me"))
}

func TestAdoptMemorySuggestionGoneMarksStale(t *testing.T) {
	svc, st := newTestService(t)
	seedMemories(t, st)
	sg, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "release-b", Action: MemoryActMerge, Reason: "重复",
		Payload: MemoryPayload{Into: "release-a"}}, "s", "")
	assert.NoErr(t, err)
	assert.NoErr(t, st.UpsertTrackerMemory(jobstore.TrackerRecord{TrackerID: "trk", ID: "release-a", Body: json.RawMessage("{}"), Rev: 9, Deleted: true}))
	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	_, ok := find(cards, "memory:1")
	assert.False(t, ok)
	_, err = svc.AdoptMemorySuggestion(sg.ID, "human:me")
	assert.True(t, errors.Is(err, ErrMemoryNotFound))
	row, err := st.GetMemorySuggestion(sg.ID)
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.MemorySuggestStale, row.State)
}
