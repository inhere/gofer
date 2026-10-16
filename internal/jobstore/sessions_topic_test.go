package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	_ "modernc.org/sqlite"
)

func seedTopicSession(t *testing.T, s *Store, sid, state string) {
	t.Helper()
	_, err := s.UpsertAgentSession(AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "p", State: state})
	assert.NoErr(t, err)
}

// 旧库（没有 topic 列）打开后补列，旧行读回空主题。
func TestMigrateAddsTopicColumnsToOldSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-sessions.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE agent_sessions (
  session_id   TEXT PRIMARY KEY,
  agent        TEXT NOT NULL,
  project_key  TEXT,
  runner       TEXT,
  cwd          TEXT,
  title        TEXT,
  transcript   TEXT,
  tmux_pane    TEXT,
  state        TEXT NOT NULL DEFAULT 'running',
  relay        INTEGER NOT NULL DEFAULT 0,
  turn_no      INTEGER NOT NULL DEFAULT 0,
  last_message TEXT,
  last_event   TEXT,
  last_seen_at INTEGER NOT NULL,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER
)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO agent_sessions (session_id, agent, state, last_seen_at, started_at) VALUES ('old-1','claude','idle',1,1)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()
	for _, col := range []string{"topic", "scope_json", "topic_source", "topic_at"} {
		assert.True(t, tableHasColumn(t, s, "agent_sessions", col), col)
	}
	got, ok, err := s.GetAgentSession("old-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", got.Topic)
	assert.Eq(t, 0, len(got.Scope))
	assert.Eq(t, "", got.TopicSource)

	// 补列后可以写主题。
	topic := "迁移后的主题"
	scope := []string{"a/b"}
	got, ok, ended, err := s.SetSessionTopic("old-1", &topic, &scope, SessionTopicHuman)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.False(t, ended)
	assert.Eq(t, topic, got.Topic)
	assert.Eq(t, []string{"a/b"}, got.Scope)
}

func TestSetSessionTopic(t *testing.T) {
	s := openTest(t)
	seedTopicSession(t, s, "s1", SessionRunning)

	topic := "gofer 开发"
	scope := []string{"inhere-tools/gofer", "docs"}
	a, ok, ended, err := s.SetSessionTopic("s1", &topic, &scope, SessionTopicSelf)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.False(t, ended)
	assert.Eq(t, topic, a.Topic)
	assert.Eq(t, scope, a.Scope)
	assert.Eq(t, SessionTopicSelf, a.TopicSource)
	assert.True(t, a.TopicAt > 0)

	// 只改 topic 不动 scope；来源随最近一次设置更新。
	topic2 := "新主题"
	a, _, _, err = s.SetSessionTopic("s1", &topic2, nil, SessionTopicHuman)
	assert.NoErr(t, err)
	assert.Eq(t, topic2, a.Topic)
	assert.Eq(t, scope, a.Scope)
	assert.Eq(t, SessionTopicHuman, a.TopicSource)

	// 空列表清空 scope。
	empty := []string{}
	a, _, _, err = s.SetSessionTopic("s1", nil, &empty, SessionTopicHuman)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(a.Scope))
	assert.Eq(t, topic2, a.Topic)

	// 再次注册（心跳/登记路径）不冲掉主题。
	_, err = s.UpsertAgentSession(AgentSession{SessionID: "s1", Agent: "claude", Title: "t"})
	assert.NoErr(t, err)
	a, _, err = s.GetAgentSession("s1")
	assert.NoErr(t, err)
	assert.Eq(t, topic2, a.Topic)

	// 未知会话、已结束会话、非法来源。
	_, ok, _, err = s.SetSessionTopic("nope", &topic, nil, SessionTopicSelf)
	assert.NoErr(t, err)
	assert.False(t, ok)
	seedTopicSession(t, s, "s2", SessionEnded)
	_, ok, ended, err = s.SetSessionTopic("s2", &topic, nil, SessionTopicSelf)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, ended)
	a, _, _ = s.GetAgentSession("s2")
	assert.Eq(t, "", a.Topic)
	_, _, _, err = s.SetSessionTopic("s1", &topic, nil, "inferred")
	assert.Err(t, err)
	_, _, _, err = s.SetSessionTopic("s1", nil, nil, SessionTopicSelf)
	assert.Err(t, err)
}
