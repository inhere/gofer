package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/authz"
)

// authGrantColumns is the column set of auth_grants (设计「数据层」). Phase 2 must not
// need another migration on this table, so the test pins every column — including the
// ones only phase 2 writes (会话 owner / 化身、孤儿、展开快照、使用计数).
var authGrantColumns = []string{
	"id", "subject", "pattern", "scope", "expires_at", "session_owner", "session_incarnation",
	"granted_by", "granted_at", "revoked_at", "revoked_by", "orphaned_at", "expand_snapshot",
	"note", "last_used_at", "use_count",
}

// authAuditColumns is the column set of auth_audit (设计「数据层」): the append-only log of
// grant changes and decisions, kept 90 days (设计决策 10).
var authAuditColumns = []string{
	"at", "actor", "action", "subject", "permission", "session_id", "grant_id", "method",
	"path", "status", "reason",
}

// authGrantsByPattern indexes grants by pattern; a seeded grant's pattern is the permission
// name itself.
func authGrantsByPattern(grants []AuthGrant) map[string]AuthGrant {
	out := make(map[string]AuthGrant, len(grants))
	for _, g := range grants {
		out[g.Pattern] = g
	}
	return out
}

// authGrantByID finds one grant by its id (the seeded rows and an administrator's rows can
// share a pattern, so a lookup by pattern is ambiguous).
func authGrantByID(grants []AuthGrant, id string) (AuthGrant, bool) {
	for _, g := range grants {
		if g.ID == id {
			return g, true
		}
	}
	return AuthGrant{}, false
}

// sessionIncarnations reads session_id -> incarnation straight from the table. The store
// has no reader for the column yet (T1.3 only stores it; the session-grant judgement that
// compares it is T2.2's), so the storage-level tests query it directly.
func sessionIncarnations(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT session_id, COALESCE(incarnation,'') FROM agent_sessions`)
	assert.NoErr(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sid, inc string
		assert.NoErr(t, rows.Scan(&sid, &inc))
		out[sid] = inc
	}
	assert.NoErr(t, rows.Err())
	return out
}

// TestAuthTablesAndSeedOnFreshDB: a brand-new database gets the three permission tables
// with ALL their columns, and — in the same migration — design §3's 16 seeded permissions
// for the `users` subject, so an upgrade keeps today's behaviour without anyone granting
// anything.
func TestAuthTablesAndSeedOnFreshDB(t *testing.T) {
	s := openTest(t)

	for _, col := range authGrantColumns {
		assert.True(t, tableHasColumn(t, s, "auth_grants", col), "auth_grants.%s", col)
	}
	for _, col := range authAuditColumns {
		assert.True(t, tableHasColumn(t, s, "auth_audit", col), "auth_audit.%s", col)
	}
	assert.True(t, tableHasColumn(t, s, "auth_meta", "key"))
	assert.True(t, tableHasColumn(t, s, "auth_meta", "value"))
	assert.True(t, tableHasColumn(t, s, "agent_sessions", "incarnation"))
	assert.True(t, indexExists(t, s, "idx_auth_grants_subject"))

	seed := authz.Seeded(authz.SeedV1)
	assert.Len(t, seed, 16)

	grants, err := s.ListAuthGrants()
	assert.NoErr(t, err)
	assert.Len(t, grants, len(seed))
	byPattern := authGrantsByPattern(grants)
	for _, p := range seed {
		g, ok := byPattern[p.Name]
		if !ok {
			t.Errorf("seeded permission %s is missing from auth_grants", p.Name)
			continue
		}
		assert.Eq(t, seedGrantID(SubjectUsers, p.Name), g.ID)
		assert.Eq(t, SubjectUsers, g.Subject)
		assert.Eq(t, "*", g.Scope)
		assert.Eq(t, seedGrantActor, g.GrantedBy)
		assert.Gt(t, g.GrantedAt, int64(0))
		assert.Eq(t, []string{p.Name}, g.ExpandSnapshot)
		assert.Eq(t, int64(0), g.ExpiresAt, "a seeded grant never expires")
		assert.Eq(t, int64(0), g.RevokedAt)
		assert.Eq(t, "", g.RevokedBy)
		assert.Eq(t, int64(0), g.OrphanedAt)
		assert.Eq(t, "", g.SessionOwner)
		assert.Eq(t, "", g.SessionIncarnation)
		assert.Eq(t, int64(0), g.UseCount)
		assert.Eq(t, int64(0), g.LastUsedAt)
	}

	// The marker and the rows land together: the seed version says exactly what was planted.
	v, err := s.AuthSeedVersion()
	assert.NoErr(t, err)
	assert.Eq(t, int64(authz.SeedV1), v)
	// The grant-table generation exists so phase 2's cache can bump it.
	gv, err := s.AuthGrantsVersion()
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), gv)

	// The read path the resolver uses: every seeded grant is active for `users`, and a
	// subject nobody granted anything to reads back empty rather than as an error.
	active, err := s.ListActiveAuthGrants(SubjectUsers)
	assert.NoErr(t, err)
	assert.Len(t, active, len(seed))
	none, err := s.ListActiveAuthGrants("caller:agent-dev")
	assert.NoErr(t, err)
	assert.Len(t, none, 0)
	_, err = s.ListActiveAuthGrants("  ")
	assert.Err(t, err, "an empty subject is a programming error, not an empty result")
}

// TestAuthSeedRunsOnceAcrossRestarts: 重启不重复. Seeding is guarded by the persistent
// `seed_version` marker written in the same transaction as the rows, so opening the same
// database again — and again — plants nothing a second time and leaves rows an
// administrator added alone.
func TestAuthSeedRunsOnceAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	seed, err := s.ListAuthGrants()
	assert.NoErr(t, err)
	assert.Len(t, seed, len(authz.Seeded(authz.SeedV1)))
	firstSeededAt := authGrantsByPattern(seed)[authz.PermJobSubmit].GrantedAt

	// A grant an administrator added later (the write interface of phase 2).
	_, err = s.db.Exec(`INSERT INTO auth_grants
  (id, subject, pattern, scope, granted_by, granted_at, use_count)
  VALUES ('g-admin','users','tunnel.*','*','default',1,0)`)
	assert.NoErr(t, err)
	assert.NoErr(t, s.Close())

	for i := 0; i < 2; i++ {
		reopened, err := Open(path)
		assert.NoErr(t, err)
		grants, err := reopened.ListAuthGrants()
		assert.NoErr(t, err)
		assert.Len(t, grants, len(seed)+1, "restart %d", i+1)
		// The seeded row kept its granted_at: nothing was re-planted.
		assert.Eq(t, firstSeededAt, authGrantsByPattern(grants)[authz.PermJobSubmit].GrantedAt)
		v, err := reopened.AuthSeedVersion()
		assert.NoErr(t, err)
		assert.Eq(t, int64(authz.SeedV1), v)
		assert.NoErr(t, reopened.Close())
	}
}

// TestAuthSeedRevokeSurvivesRestart: 撤销后重启不还原. Revoking a seeded permission is a
// write of revoked_at (the row stays for the audit trail); reopening the database must not
// plant it again, because the seed version was already written — that is the whole point
// of「种入只做一次」.
func TestAuthSeedRevokeSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gofer.db")
	s, err := Open(path)
	assert.NoErr(t, err)
	_, err = s.db.Exec(`UPDATE auth_grants SET revoked_at=?, revoked_by='default' WHERE id=?`,
		1234, seedGrantID(SubjectUsers, authz.PermJobSubmit))
	assert.NoErr(t, err)
	assert.NoErr(t, s.Close())

	reopened, err := Open(path)
	assert.NoErr(t, err)
	defer reopened.Close()

	all, err := reopened.ListAuthGrants()
	assert.NoErr(t, err)
	assert.Len(t, all, len(authz.Seeded(authz.SeedV1)), "a revoke must not add a row back")
	revoked := authGrantsByPattern(all)[authz.PermJobSubmit]
	assert.Eq(t, int64(1234), revoked.RevokedAt)
	assert.Eq(t, "default", revoked.RevokedBy)

	active, err := reopened.ListActiveAuthGrants(SubjectUsers)
	assert.NoErr(t, err)
	assert.Len(t, active, len(authz.Seeded(authz.SeedV1))-1)
	for _, g := range active {
		assert.Neq(t, authz.PermJobSubmit, g.Pattern, "the revoked permission is still active")
	}
}

// TestAuthMigrationFromOldDB: 从旧库迁移. A database written by a binary that knew neither
// the permission tables nor the incarnation column opens with the tables, the seed and a
// non-empty incarnation per pre-existing session.
func TestAuthMigrationFromOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE jobs (
  id           TEXT PRIMARY KEY,
  project_key  TEXT NOT NULL,
  agent        TEXT NOT NULL,
  runner       TEXT NOT NULL,
  worker_id    TEXT,
  status       TEXT NOT NULL,
  exit_code    INTEGER NOT NULL DEFAULT 0,
  cwd          TEXT,
  result_dir   TEXT NOT NULL,
  request_json TEXT,
  error        TEXT,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  updated_at   INTEGER NOT NULL
)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO jobs (id, project_key, agent, runner, status, result_dir, started_at, updated_at)
  VALUES ('old-job', 'proj', 'claude', 'local', 'done', '/tmp/old-job', 5, 5)`)
	assert.NoErr(t, err)
	// The pre-incarnation agent_sessions shape (every column the previous release
	// wrote; migrateAgentSessions adds the rest).
	_, err = raw.Exec(`CREATE TABLE agent_sessions (
  session_id   TEXT PRIMARY KEY,
  agent        TEXT NOT NULL,
  project_key  TEXT,
  runner       TEXT,
  cwd          TEXT,
  title        TEXT,
  transcript   TEXT,
  tmux_pane    TEXT,
  caller_id    TEXT,
  state        TEXT NOT NULL DEFAULT 'running',
  relay_mode   TEXT NOT NULL DEFAULT 'auto',
  relay        INTEGER NOT NULL DEFAULT 0,
  idle_sec     INTEGER,
  last_human_at INTEGER NOT NULL DEFAULT 0,
  turn_no      INTEGER NOT NULL DEFAULT 0,
  last_message TEXT,
  last_event   TEXT,
  last_seen_at INTEGER NOT NULL,
  started_at   INTEGER NOT NULL,
  ended_at     INTEGER,
  handed_off_job_id TEXT,
  handed_off_at     INTEGER,
  peer_name         TEXT,
  peer_status       TEXT,
  peer_messaging    INTEGER NOT NULL DEFAULT 0,
  progress_text     TEXT,
  last_cwd          TEXT,
  usage_json        TEXT,
  progress_at       INTEGER NOT NULL DEFAULT 0,
  usage_at          INTEGER NOT NULL DEFAULT 0,
  refused_caller_id TEXT,
  refused_at        INTEGER NOT NULL DEFAULT 0
)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO agent_sessions (session_id, agent, project_key, caller_id, state, last_seen_at, started_at)
  VALUES ('sess-a','claude','proj','default','running',7,7), ('sess-b','claude','proj','default','ended',8,8)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	for _, col := range authGrantColumns {
		assert.True(t, tableHasColumn(t, s, "auth_grants", col), "auth_grants.%s after migration", col)
	}
	for _, col := range authAuditColumns {
		assert.True(t, tableHasColumn(t, s, "auth_audit", col), "auth_audit.%s after migration", col)
	}
	grants, err := s.ListAuthGrants()
	assert.NoErr(t, err)
	assert.Len(t, grants, len(authz.Seeded(authz.SeedV1)))
	v, err := s.AuthSeedVersion()
	assert.NoErr(t, err)
	assert.Eq(t, int64(authz.SeedV1), v)

	assert.True(t, tableHasColumn(t, s, "agent_sessions", "incarnation"))
	inc := sessionIncarnations(t, s)
	assert.Len(t, inc, 2)
	assert.True(t, inc["sess-a"] != "", "a pre-existing session was left without an incarnation")
	assert.True(t, inc["sess-b"] != "", "a pre-existing session was left without an incarnation")
	assert.Neq(t, inc["sess-a"], inc["sess-b"], "each session needs its own incarnation")

	// The old data is still there: the migration adds, it never rewrites.
	job, ok, err := s.GetJob("old-job")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "done", job.Status)
}

// TestIncarnationBackfillIsIdempotent: 回填幂等. The backfill runs on EVERY Open (not only
// when the column is first added), so it also covers the sessions an older binary registers
// after a rollback — but it must never touch a session that already has an incarnation,
// and repeating it must change nothing.
func TestIncarnationBackfillIsIdempotent(t *testing.T) {
	s := openTest(t)

	// A session registered through the public write path: T1.3 stores the column but no
	// writer fills it yet, so the row lands with the empty default.
	_, err := s.UpsertAgentSession(AgentSession{SessionID: "s-api", Agent: "claude", Cwd: "/w"})
	assert.NoErr(t, err)
	// A session an older binary (rolled back) wrote, and one that already carries an
	// incarnation from a live newer process.
	_, err = s.db.Exec(`INSERT INTO agent_sessions (session_id, agent, state, last_seen_at, started_at, incarnation)
  VALUES ('s-old','claude','running',10,10,'')`)
	assert.NoErr(t, err)
	_, err = s.db.Exec(`INSERT INTO agent_sessions (session_id, agent, state, last_seen_at, started_at, incarnation)
  VALUES ('s-live','claude','running',10,10,'keepthisvalue')`)
	assert.NoErr(t, err)

	// migrate is the startup step that carries the backfill.
	assert.NoErr(t, s.migrate())
	first := sessionIncarnations(t, s)
	assert.True(t, first["s-api"] != "", "a session without an incarnation was not backfilled")
	assert.True(t, first["s-old"] != "", "a session the old binary wrote was not backfilled")
	assert.Eq(t, "keepthisvalue", first["s-live"], "an existing incarnation must never be regenerated")
	assert.Neq(t, first["s-api"], first["s-old"], "each session needs its own incarnation")

	// Idempotent: two more passes keep every value exactly as it was.
	for i := 0; i < 2; i++ {
		assert.NoErr(t, s.migrate())
	}
	again := sessionIncarnations(t, s)
	assert.Len(t, again, len(first))
	for sid, want := range first {
		assert.Eq(t, want, again[sid], "session %s after another migration pass", sid)
	}
}

// TestAuthGrantRoundTripAndFilters: the read paths report every column of a grant, list
// revoked and orphaned rows (the audit view needs them) and keep them out of the active
// set a resolver may use.
func TestAuthGrantRoundTripAndFilters(t *testing.T) {
	s := openTest(t)

	for _, stmt := range []string{
		`INSERT INTO auth_grants
  (id, subject, pattern, scope, expires_at, session_owner, session_incarnation, granted_by, granted_at,
   revoked_at, revoked_by, orphaned_at, expand_snapshot, note, last_used_at, use_count)
  VALUES ('g-caller','caller:agent-dev','tunnel.*','*',1700000000,NULL,NULL,'default',1000,
   NULL,NULL,NULL,'["tunnel.open"]','release tunnel',1700000500,3)`,
		`INSERT INTO auth_grants
  (id, subject, pattern, scope, expires_at, session_owner, session_incarnation, granted_by, granted_at,
   revoked_at, revoked_by, orphaned_at, expand_snapshot, note, last_used_at, use_count)
  VALUES ('g-session','session:s1','job.read.others','*',1700000600,'default','inc-1','default',1100,
   1200,'default',NULL,'["job.read.others"]',NULL,NULL,0)`,
		`INSERT INTO auth_grants
  (id, subject, pattern, scope, expires_at, session_owner, session_incarnation, granted_by, granted_at,
   revoked_at, revoked_by, orphaned_at, expand_snapshot, note, last_used_at, use_count)
  VALUES ('g-orphan','caller:gone','config.read','*',NULL,NULL,NULL,'default',1300,
   NULL,NULL,1400,'["config.read"]',NULL,NULL,0)`,
	} {
		_, err := s.db.Exec(stmt)
		assert.NoErr(t, err)
	}

	all, err := s.ListAuthGrants()
	assert.NoErr(t, err)
	assert.Len(t, all, len(authz.Seeded(authz.SeedV1))+3)
	byPattern := authGrantsByPattern(all)
	caller := byPattern["tunnel.*"]
	assert.Eq(t, "g-caller", caller.ID)
	assert.Eq(t, "caller:agent-dev", caller.Subject)
	assert.Eq(t, int64(1700000000), caller.ExpiresAt)
	assert.Eq(t, "default", caller.GrantedBy)
	assert.Eq(t, int64(1000), caller.GrantedAt)
	assert.Eq(t, []string{"tunnel.open"}, caller.ExpandSnapshot)
	assert.Eq(t, "release tunnel", caller.Note)
	assert.Eq(t, int64(1700000500), caller.LastUsedAt)
	assert.Eq(t, int64(3), caller.UseCount)
	assert.Eq(t, int64(0), caller.RevokedAt)

	session, ok := authGrantByID(all, "g-session")
	assert.True(t, ok)
	assert.Eq(t, "session:s1", session.Subject)
	assert.Eq(t, "default", session.SessionOwner)
	assert.Eq(t, "inc-1", session.SessionIncarnation)
	assert.Eq(t, int64(1200), session.RevokedAt)
	assert.Eq(t, "default", session.RevokedBy)
	assert.Eq(t, []string{"job.read.others"}, session.ExpandSnapshot)

	orphan, ok := authGrantByID(all, "g-orphan")
	assert.True(t, ok)
	assert.Eq(t, "caller:gone", orphan.Subject)
	assert.Eq(t, int64(1400), orphan.OrphanedAt)

	// Revoked and orphaned rows stay out of the active set; the surviving caller grant is
	// the only one of the three.
	active, err := s.ListActiveAuthGrants("caller:agent-dev")
	assert.NoErr(t, err)
	assert.Len(t, active, 1)
	assert.Eq(t, "g-caller", active[0].ID)
	activeSession, err := s.ListActiveAuthGrants("session:s1")
	assert.NoErr(t, err)
	assert.Len(t, activeSession, 0)
	activeGone, err := s.ListActiveAuthGrants("caller:gone")
	assert.NoErr(t, err)
	assert.Len(t, activeGone, 0)

	// Rows come back in grant order, and the seeded group (all planted at the same moment)
	// is not re-ordered relative to itself beyond the id tiebreak.
	for i := 1; i < len(all); i++ {
		assert.Gte(t, all[i].GrantedAt, all[i-1].GrantedAt, "grants are not in grant order")
	}
}
