package jobstore

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

// TestStoreDBStatsReportsFileAndRowCounts: DBStats 报告 db/wal 文件大小、页几何与
// 各表行数；空表报 0（键必须存在，前端的固定列才不会塌）。
func TestStoreDBStatsReportsFileAndRowCounts(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(sampleJob("job-1", "proj", 100)))
	assert.NoErr(t, s.UpsertJob(sampleJob("job-2", "proj", 101)))
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "sid-1", Agent: "claude"})
	assert.NoErr(t, err)

	st, err := s.DBStats(time.Minute)
	assert.NoErr(t, err)

	assert.False(t, st.Partial)
	assert.True(t, filepath.IsAbs(st.Path), "path should be absolute: %q", st.Path)
	assert.True(t, strings.HasSuffix(st.Path, "gofer.db"), "path=%q", st.Path)
	assert.True(t, st.SizeBytes > 0, "size_bytes=%d", st.SizeBytes)
	assert.True(t, st.PageSize > 0, "page_size=%d", st.PageSize)
	assert.True(t, st.PageCount > 0, "page_count=%d", st.PageCount)
	assert.Eq(t, int64(2), st.Tables["jobs"])
	assert.Eq(t, int64(1), st.Tables["agent_sessions"])
	ev, ok := st.Tables["job_events"]
	assert.True(t, ok, "an empty table must still be reported: %v", st.Tables)
	assert.Eq(t, int64(0), ev)
}

// TestStoreDBStatsPartialOnExhaustedBudget: 预算耗尽 → 行数不再查询、partial=true，
// 但廉价的文件/页信息照旧（调用方据此降级展示而不是报错）。
func TestStoreDBStatsPartialOnExhaustedBudget(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(sampleJob("job-1", "proj", 100)))

	st, err := s.DBStats(0)
	assert.NoErr(t, err)

	assert.True(t, st.Partial)
	assert.Eq(t, 0, len(st.Tables))
	assert.True(t, st.SizeBytes > 0, "size_bytes=%d", st.SizeBytes)
	assert.True(t, st.PageCount > 0, "page_count=%d", st.PageCount)
}

// TestStoreSessionStatsCountsLiveWork: 只数"还没人答"的 relay turn（OPEN 且在
// 超时窗口内，其它 kind/状态的 decision 不算），并只把 1 小时内见过的 session
// 计入 seen_within_1h。
func TestStoreSessionStatsCountsLiveWork(t *testing.T) {
	s := openTest(t)
	now := time.Now().Unix()

	for _, in := range []AgentSession{
		{SessionID: "sess-run", Agent: "claude", State: SessionRunning, RelayMode: RelayModeOn},
		{SessionID: "sess-wait", Agent: "codex", State: SessionWaitingReply, RelayMode: RelayModeAuto},
		{SessionID: "sess-stale", Agent: "claude", State: SessionEnded, RelayMode: RelayModeOff},
	} {
		i := in
		_, err := s.UpsertAgentSession(i)
		assert.NoErr(t, err)
	}
	// Upsert stamps last_seen_at = now; backdate one row past the 1h window.
	_, err := s.db.Exec(`UPDATE agent_sessions SET last_seen_at = ? WHERE session_id = ?`,
		now-7200, "sess-stale")
	assert.NoErr(t, err)

	for _, d := range []*PlanDecision{
		// waiting: relay turn, OPEN, deadline in the future
		{ID: "dec-open", Title: "t", Question: "q", TimeoutSec: 600, SessionID: "sess-wait", Kind: DecisionKindRelay},
		// past its deadline (lazy expiry has not run): not waiting
		{ID: "dec-expired", Title: "t", Question: "q", TimeoutSec: 2, AskedAt: now - 100,
			SessionID: "sess-wait", Kind: DecisionKindRelay},
		// answered: not waiting
		{ID: "dec-answered", Title: "t", Question: "q", TimeoutSec: 600,
			State: DecisionAnswered, SessionID: "sess-wait", Kind: DecisionKindRelay},
		// a plain gofer_ask_human decision (no kind): not a relay turn
		{ID: "dec-plain", Title: "t", Question: "q", TimeoutSec: 600},
	} {
		assert.NoErr(t, s.InsertDecision(d))
	}

	st, err := s.SessionStats(now)
	assert.NoErr(t, err)

	assert.Eq(t, 3, st.Total)
	assert.Eq(t, 1, st.ByState[SessionRunning])
	assert.Eq(t, 1, st.ByState[SessionWaitingReply])
	assert.Eq(t, 1, st.ByState[SessionEnded])
	assert.Eq(t, 0, st.ByState[SessionIdle])
	assert.Eq(t, 1, st.ByRelayMode[RelayModeOn])
	assert.Eq(t, 1, st.ByRelayMode[RelayModeAuto])
	assert.Eq(t, 1, st.ByRelayMode[RelayModeOff])
	assert.Eq(t, 1, st.WaitingTurns)
	assert.Eq(t, 2, st.SeenWithin1h)
}
