package work

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestActorLabels(t *testing.T) {
	assert.Eq(t, "human:alice", HumanBy("alice"))
	assert.Eq(t, "human", HumanBy(" "))
	assert.Eq(t, "session:s1(claude)", SessionBy("s1", "claude"))
	assert.Eq(t, "summarizer(claude)", SummarizerBy("claude"))
	assert.Eq(t, "steward", StewardBy(""))
	assert.Eq(t, ActorSession, ActorKind("session:s1(claude)"))
	assert.Eq(t, ActorHuman, ActorKind("human:bob"))
	assert.Eq(t, ActorSummarizer, ActorKind("summarizer(claude)"))
	assert.Eq(t, ActorSteward, ActorKind("steward(codex-acp)"))
	assert.Eq(t, ActorJob, ActorKind("job:j1"))
	assert.Eq(t, ActorSystem, ActorKind(""))
	sid, agent, ok := SessionOf("session:abc(codex)")
	assert.True(t, ok)
	assert.Eq(t, "abc", sid)
	assert.Eq(t, "codex", agent)
	_, _, ok = SessionOf("human:x")
	assert.False(t, ok)
}

func TestFieldSourcesFollowWhoWroteEachField(t *testing.T) {
	svc, st, _ := newSvc(t)
	session(t, st, "sess-src00001", jobstore.SessionRunning)
	w, err := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t", Goal: "g0", SessionIDs: []string{"sess-src00001"}, By: "human:alice"})
	assert.NoErr(t, err)

	// A bare session label is completed with the agent; each field remembers its writer.
	_, err = svc.Report(w.ID, ReportInput{Blocker: "缺设备", Next: "接线", By: "session:sess-src00001"})
	assert.NoErr(t, err)
	goal := "新目标"
	_, err = svc.Update(w.ID, jobstore.WorkItemPatch{Goal: &goal}, 0, "human:bob")
	assert.NoErr(t, err)

	d, err := svc.Detail(w.ID, 50)
	assert.NoErr(t, err)
	assert.Eq(t, "human:bob", d.FieldSources[jobstore.WorkFieldGoal].By)
	assert.Eq(t, "session:sess-src00001(claude)", d.FieldSources[jobstore.WorkFieldBlocker].By)
	assert.Eq(t, "session:sess-src00001(claude)", d.FieldSources[jobstore.WorkFieldNext].By)
	_, hasSummary := d.FieldSources[jobstore.WorkFieldSummary]
	assert.False(t, hasSummary)
	last := d.Journal[len(d.Journal)-1]
	assert.Eq(t, "human:bob", last.By)

	// The creator is the source of the initial goal.
	w2, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "t2", Goal: "g", By: "human:alice"})
	src, _ := st.WorkFieldSources(w2.ID)
	assert.Eq(t, "human:alice", src[jobstore.WorkFieldGoal].By)
}
