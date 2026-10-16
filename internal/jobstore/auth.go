package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/authz"
)

// 权限与授权的存储（设计 docs/design/2026-10-14-db-capabilities-and-session-grants-design.md
// §2「授权模型与判定」、§3「配置推出的授权（真值表）与数据库种入」与「数据层」）。
//
// 三张表一次建齐 —— auth_grants（含会话、孤儿、展开快照、使用计数各列）、auth_meta、
// auth_audit —— 并且和设计 §3 的种入放在同一个迁移事务里（migrateAuth）：一个数据库不可能
// 只建出表而没有种子，也不可能种了一半。行只增不改：撤销授权是写 revoked_at、调用方消失是写
// orphaned_at，那行永远留着（审计和「撤销后不还原」都靠它），只有删库才会丢。
//
// 判定不在这一层：本文件只存取事实（授权行、版本号）。权限名的展开、配置推出的授权、会话主体
// 的 owner 与化身比对，都在 internal/authz 与入口层完成（G022：数据层不反向依赖入口层，也不
// 替它决定策略）。

// auth_meta 键（设计「数据层」）。
const (
	// AuthMetaSeedVersion 是让种入「只做一次」的持久标记（设计 §3）：这个数据库已经为
	// `users` 主体种到的最高种入版本。它和种入的行在同一个事务里写，所以它不会撒谎。
	AuthMetaSeedVersion = "seed_version"
	// AuthMetaGrantsVersion 是授权表的代号。入口层的授权缓存按它判断是否失效（设计
	// 「一致性」：本进程写入后立即刷新，其他进程按 5 秒 TTL 失效）；每次改动授权的写接口
	// 都要递增它，本批只负责把这个键建出来。
	AuthMetaGrantsVersion = "grants_version"
)

// SubjectUsers 是 `users` 主体（设计 §2）：对所有 user 类调用方都成立的授权。另外两种主体拼写
// `caller:<id>` 与 `session:<sid>` 由第二批的授权管理接口写入。
const SubjectUsers = "users"

// seedGrantActor 是迁移种下的授权的 granted_by 值（设计 §3）。
const seedGrantActor = "seed"

// authSchemaStmts 是三张权限表的 DDL（设计「数据层」）。它们是新表，没有旧库要 ALTER，所以第二批
// 需要的列一次给全 —— 含会话主体要的 owner / 化身、孤儿标记、展开快照与使用计数。
var authSchemaStmts = []string{
	`CREATE TABLE IF NOT EXISTS auth_grants (
  id                  TEXT PRIMARY KEY,
  subject             TEXT NOT NULL,
  pattern             TEXT NOT NULL,
  scope               TEXT NOT NULL DEFAULT '*',
  expires_at          INTEGER,
  session_owner       TEXT,
  session_incarnation TEXT,
  granted_by          TEXT NOT NULL DEFAULT '',
  granted_at          INTEGER NOT NULL,
  revoked_at          INTEGER,
  revoked_by          TEXT,
  orphaned_at         INTEGER,
  expand_snapshot     TEXT,
  note                TEXT,
  last_used_at        INTEGER,
  use_count           INTEGER NOT NULL DEFAULT 0
)`,
	// 解析器按主体取授权（subject=users 的那条读路径），也是 web 上按主体分组的读路径。
	`CREATE INDEX IF NOT EXISTS idx_auth_grants_subject ON auth_grants(subject)`,
	// auth_meta 是键值表：种入标记与授权表代号（设计「数据层」）。
	`CREATE TABLE IF NOT EXISTS auth_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
)`,
	// auth_audit 是授权与判定的追加日志（第二批写入、按设计决策 10 保留 90 天）。只追加，
	// 不改不删（保留期清理除外）。
	`CREATE TABLE IF NOT EXISTS auth_audit (
  at         INTEGER NOT NULL,
  actor      TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  subject    TEXT NOT NULL DEFAULT '',
  permission TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  grant_id   TEXT NOT NULL DEFAULT '',
  method     TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL DEFAULT '',
  status     INTEGER NOT NULL DEFAULT 0,
  reason     TEXT NOT NULL DEFAULT ''
)`,
}

// migrateAuth 建出三张权限表，并在同一个事务里种下设计 §3 的授权。建表和种入共用一个事务是
// 设计要求（「用持久标记 auth_meta(seed_version) 判断，和建表放在同一个迁移事务里」）：要么表
// 和种子一起出现，要么都不出现，不存在「表在、种子没了、重启又种一遍」的中间态。
//
// 每次 Open 调用，完全幂等：种过就不再种。
func (s *Store) migrateAuth() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin auth migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range authSchemaStmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("jobstore: apply auth schema: %w", err)
		}
	}
	if err := seedAuthGrants(tx, s.unixNow()); err != nil {
		return err
	}
	return tx.Commit()
}

// seedAuthGrants 种下所有比已记录的标记更新的种入版本（设计 §3「数据库种入」）。
//
// 种入只做一次：标记写进同一个事务后，本函数再也不会种这个版本。这正是「管理员收紧过的授权不会
// 被重新放开」和「撤销后重启不还原」的实现 —— 撤销只是给行写 revoked_at，重启时没有任何 INSERT
// 会把它加回来。以后目录新增「原本无检查」的权限时，种入版本递增，只补种新增的那几项。
func seedAuthGrants(tx *sql.Tx, now int64) error {
	stored := 0
	if v, ok, err := authMetaValue(tx, AuthMetaSeedVersion); err != nil {
		return err
	} else if ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("jobstore: auth_meta.%s = %q is not a version", AuthMetaSeedVersion, v)
		}
		stored = n
	}
	for v := stored + 1; v <= latestSeedVersion(); v++ {
		for _, p := range authz.Seeded(v) {
			// 展开快照：种入的模式就是权限名本身，展开结果只有它自己（设计 §2「保存时模式
			// 必须至少匹配一项，并保存展开快照」）。目录升级后 web 用它提示「有新权限待确认」。
			snapshot, err := json.Marshal([]string{p.Name})
			if err != nil {
				return fmt.Errorf("jobstore: seed auth grant %s: %w", p.Name, err)
			}
			if _, err := tx.Exec(`INSERT INTO auth_grants
  (id, subject, pattern, scope, expires_at, session_owner, session_incarnation,
   granted_by, granted_at, revoked_at, revoked_by, orphaned_at, expand_snapshot, note,
   last_used_at, use_count)
  VALUES (?,?,?,'*',NULL,NULL,NULL,?,?,NULL,NULL,NULL,?,NULL,NULL,0)`,
				seedGrantID(SubjectUsers, p.Name), SubjectUsers, p.Name,
				seedGrantActor, now, string(snapshot)); err != nil {
				return fmt.Errorf("jobstore: seed auth grant %s: %w", p.Name, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO auth_meta(key, value) VALUES (?,?)
  ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			AuthMetaSeedVersion, strconv.Itoa(v)); err != nil {
			return fmt.Errorf("jobstore: write auth_meta.%s: %w", AuthMetaSeedVersion, err)
		}
	}
	// 授权表代号：本版本打开的每个数据库都有这一行，第二批的缓存递增它时不必再造键。
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO auth_meta(key, value) VALUES (?, '0')`, AuthMetaGrantsVersion); err != nil {
		return fmt.Errorf("jobstore: write auth_meta.%s: %w", AuthMetaGrantsVersion, err)
	}
	return nil
}

// seedGrantID 是种入授权的确定性 id：由主体与模式拼成，所以同一个权限在哪个数据库里都是同一行
// （重新种入也撞不出重复行）。前缀让它不会和授权管理接口（第二批）随机生成的 id 撞上。
func seedGrantID(subject, pattern string) string {
	return "seed:" + subject + ":" + pattern
}

// latestSeedVersion 是目录里最高的种入版本。种入从标记的下一个版本走到底，所以跳版本升级的数据库
// 也会把中间的每一组补齐（设计 §3「以后目录新增『原本无检查』的权限时，seed_version 递增，只种
// 新增的那几项」）。
func latestSeedVersion() int {
	latest := 0
	for _, p := range authz.Permissions() {
		if p.SeedVersion > latest {
			latest = p.SeedVersion
		}
	}
	return latest
}

// AuthGrant 是 auth_grants 的一行：发给某一个主体的一条模式授权（设计 §2）。
//
// 存储层只报事实，不判定：到期要看调用方的时钟，会话授权还要比对会话当前的 owner 与化身，孤儿要
// 看配置里还有没有这个调用方 —— 这些都在入口层（第一批的解析器只读 subject=users 且未撤销的行）。
type AuthGrant struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Pattern 是权限名模式（`tunnel.*` / `job.*.others` / `*`）。
	Pattern string `json:"pattern"`
	// Scope 第一期恒为 `*`；第二期才允许 project:<glob> 这样的限定（设计决策 10）。
	Scope string `json:"scope"`
	// ExpiresAt 是到期时刻（unix 秒；0 = 不过期）。会话主体必填，最长 24 小时（设计 §4）。
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// SessionOwner / SessionIncarnation 只对会话主体有值：授权时会话的 owner 与化身。会话的
	// 当前 owner 或化身一变，这条授权就不再生效（设计 §4）。
	SessionOwner       string `json:"session_owner,omitempty"`
	SessionIncarnation string `json:"session_incarnation,omitempty"`
	// GrantedBy 是写入者：`seed` 表示迁移种入，其余是调用方 id（第二批的接口）。
	GrantedBy string `json:"granted_by"`
	GrantedAt int64  `json:"granted_at"`
	// RevokedAt / RevokedBy 记录撤销（设计 §2：撤销后重启不还原）；0 / "" 表示仍然生效。
	RevokedAt int64  `json:"revoked_at,omitempty"`
	RevokedBy string `json:"revoked_by,omitempty"`
	// OrphanedAt 是授权被判为孤儿的时刻（caller:<id> 在配置里不存在时停用，设计 §2）；0 表示
	// 不是孤儿。
	OrphanedAt int64 `json:"orphaned_at,omitempty"`
	// ExpandSnapshot 是这条模式在保存时展开出的权限名（设计 §2）。目录升级后，如果模式能匹配到
	// 快照里没有的新权限，web 上提示「有新权限待确认」。
	ExpandSnapshot []string `json:"expand_snapshot"`
	Note           string   `json:"note,omitempty"`
	// LastUsedAt / UseCount 是使用计数（第二批写入、按设计「审计与使用计数」统计）。
	LastUsedAt int64 `json:"last_used_at,omitempty"`
	UseCount   int64 `json:"use_count"`
}

const selectAuthGrantCols = `SELECT id, subject, pattern, COALESCE(scope,''), COALESCE(expires_at,0),
  COALESCE(session_owner,''), COALESCE(session_incarnation,''), COALESCE(granted_by,''), granted_at,
  COALESCE(revoked_at,0), COALESCE(revoked_by,''), COALESCE(orphaned_at,0),
  COALESCE(expand_snapshot,''), COALESCE(note,''), COALESCE(last_used_at,0), use_count
  FROM auth_grants`

func scanAuthGrant(sc rowScanner) (AuthGrant, error) {
	var (
		g        AuthGrant
		snapshot string
	)
	err := sc.Scan(&g.ID, &g.Subject, &g.Pattern, &g.Scope, &g.ExpiresAt,
		&g.SessionOwner, &g.SessionIncarnation, &g.GrantedBy, &g.GrantedAt,
		&g.RevokedAt, &g.RevokedBy, &g.OrphanedAt,
		&snapshot, &g.Note, &g.LastUsedAt, &g.UseCount)
	if err != nil {
		return AuthGrant{}, err
	}
	if strings.TrimSpace(snapshot) != "" {
		if err := json.Unmarshal([]byte(snapshot), &g.ExpandSnapshot); err != nil {
			return AuthGrant{}, fmt.Errorf("jobstore: grant %q: expand_snapshot is not a JSON array: %w", g.ID, err)
		}
	}
	return g, nil
}

// ListAuthGrants 返回全部授权行，撤销的与孤儿的都在内（审计视图要它们），按授权时间从早到晚。
func (s *Store) ListAuthGrants() ([]AuthGrant, error) {
	rows, err := s.db.Query(selectAuthGrantCols + ` ORDER BY granted_at, id`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list auth grants: %w", err)
	}
	defer rows.Close()
	return scanAuthGrants(rows)
}

// ListActiveAuthGrants 返回某一个主体里未被撤销、也不是孤儿的授权 —— 计算有效权限时只该看这些行
// （设计 §2：孤儿授权「标为孤儿并停用」）。到期与否由调用方按自己的时钟判定，会话主体的 owner 与
// 化身比对同样在调用方（设计 §4）。
func (s *Store) ListActiveAuthGrants(subject string) ([]AuthGrant, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("jobstore: list active auth grants: empty subject")
	}
	rows, err := s.db.Query(
		selectAuthGrantCols+` WHERE subject = ? AND revoked_at IS NULL AND orphaned_at IS NULL
  ORDER BY granted_at, id`, subject)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list active auth grants for %q: %w", subject, err)
	}
	defer rows.Close()
	return scanAuthGrants(rows)
}

// ListActiveAuthGrantsAll 返回全部**生效**的授权：未被撤销、不是孤儿，主体不限。这是入口层的授权缓存
// 每 5 秒重读的那条查询（设计「一致性」）：一次读完 users 与每个 caller:<id> 的授权，再按主体分组，
// 而不是按主体逐个查询。到期与否仍由调用方按自己的时钟判定。
func (s *Store) ListActiveAuthGrantsAll() ([]AuthGrant, error) {
	rows, err := s.db.Query(selectAuthGrantCols +
		` WHERE revoked_at IS NULL AND orphaned_at IS NULL ORDER BY subject, granted_at, id`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list active auth grants: %w", err)
	}
	defer rows.Close()
	return scanAuthGrants(rows)
}

// scanAuthGrants 扫描一个 auth_grants 查询的全部行（三个列表接口共用；取列顺序由 selectAuthGrantCols
// 固定，扫描必须一一对应）。
func scanAuthGrants(rows *sql.Rows) ([]AuthGrant, error) {
	var out []AuthGrant
	for rows.Next() {
		g, err := scanAuthGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan auth grant: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list auth grants: %w", err)
	}
	return out, nil
}

// GetAuthGrant 读一条授权行；不存在时 ok 为 false、err 为 nil。
func (s *Store) GetAuthGrant(id string) (AuthGrant, bool, error) {
	g, err := scanAuthGrant(s.db.QueryRow(selectAuthGrantCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuthGrant{}, false, nil
	}
	if err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: get auth grant %q: %w", id, err)
	}
	return g, true, nil
}

// CreateAuthGrant 写一条数据库授权（设计「接口」：新增）。存储层在这里收口三件事：
//
//   - 展开：模式按主体种类经 authz.Expand 展开（任何模式匹配不到 auth.manage、通配符不命中危险级、
//     会话主体只展开到可授予会话的项），**展开快照**与行一起写入 —— 设计 §2 要求「保存时模式必须至少
//     匹配一项」，所以展开不出任何权限时直接报错（authz.ErrNoMatch），入口层据此回 400；
//   - 代号：授权表代号与这一行在同一个事务里递增，入口层的缓存据此失效（设计「一致性」）；
//   - id 与时间：id 为空时生成，granted_at 用存储自己的时钟。
//
// scope 留空时写 `*`（第一期只允许 `*`，设计决策 10）。传入行的撤销 / 孤儿 / 使用计数各列不参与写入：
// 新行一定是生效、非孤儿、未使用。
func (s *Store) CreateAuthGrant(g AuthGrant) (AuthGrant, error) {
	if strings.TrimSpace(g.Subject) == "" {
		return AuthGrant{}, errors.New("jobstore: create auth grant: empty subject")
	}
	if strings.TrimSpace(g.Pattern) == "" {
		return AuthGrant{}, errors.New("jobstore: create auth grant: empty pattern")
	}
	kind, _, err := authz.ParseSubject(g.Subject)
	if err != nil {
		return AuthGrant{}, fmt.Errorf("jobstore: create auth grant: %w", err)
	}
	exp, err := authz.Expand(g.Pattern, kind)
	if err != nil {
		return AuthGrant{}, fmt.Errorf("jobstore: create auth grant: %w", err)
	}
	snapshot, err := json.Marshal(exp.Snapshot())
	if err != nil {
		return AuthGrant{}, fmt.Errorf("jobstore: create auth grant: %w", err)
	}
	g.ID = strings.TrimSpace(g.ID)
	if g.ID == "" {
		g.ID = newAuthGrantID()
	}
	if strings.TrimSpace(g.Scope) == "" {
		g.Scope = "*"
	}
	g.GrantedAt = s.unixNow()
	g.RevokedAt, g.RevokedBy, g.OrphanedAt = 0, "", 0
	g.LastUsedAt, g.UseCount = 0, 0
	g.ExpandSnapshot = exp.Snapshot()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return AuthGrant{}, fmt.Errorf("jobstore: begin create auth grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO auth_grants
  (id, subject, pattern, scope, expires_at, session_owner, session_incarnation,
   granted_by, granted_at, revoked_at, revoked_by, orphaned_at, expand_snapshot, note,
   last_used_at, use_count)
  VALUES (?,?,?,?,?,?,?,?,?,NULL,NULL,NULL,?,?,NULL,0)`,
		g.ID, g.Subject, g.Pattern, g.Scope, nullableInt(g.ExpiresAt), nullableText(g.SessionOwner),
		nullableText(g.SessionIncarnation), g.GrantedBy, g.GrantedAt, string(snapshot), nullableText(g.Note)); err != nil {
		return AuthGrant{}, fmt.Errorf("jobstore: create auth grant %s: %w", g.ID, err)
	}
	if _, err := bumpAuthGrantsVersion(tx); err != nil {
		return AuthGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuthGrant{}, fmt.Errorf("jobstore: commit create auth grant: %w", err)
	}
	return g, nil
}

// RevokeAuthGrant 撤销一条授权：写 revoked_at / revoked_by（设计 §2「撤销」，那行永远留着）。已经撤销
// 过的行是幂等的空操作，返回它现在的样子；不存在时 ok 为 false。
func (s *Store) RevokeAuthGrant(id, by string) (AuthGrant, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: begin revoke auth grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE auth_grants SET revoked_at=?, revoked_by=? WHERE id=? AND revoked_at IS NULL`,
		s.unixNow(), by, id)
	if err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: revoke auth grant %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		if _, err := bumpAuthGrantsVersion(tx); err != nil {
			return AuthGrant{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: commit revoke auth grant: %w", err)
	}
	return s.GetAuthGrant(id)
}

// ActivateAuthGrant 恢复一条孤儿授权：清掉 orphaned_at（设计 §2「要根管理员在 web 上确认后才恢复」）。
// 不是孤儿的行是幂等的空操作，返回它现在的样子；不存在时 ok 为 false。
func (s *Store) ActivateAuthGrant(id string) (AuthGrant, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: begin activate auth grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE auth_grants SET orphaned_at=NULL WHERE id=? AND orphaned_at IS NOT NULL`, id)
	if err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: activate auth grant %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		if _, err := bumpAuthGrantsVersion(tx); err != nil {
			return AuthGrant{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AuthGrant{}, false, fmt.Errorf("jobstore: commit activate auth grant: %w", err)
	}
	return s.GetAuthGrant(id)
}

// OrphanAuthGrants 把列出的授权标为孤儿（设计 §2「孤儿授权」）：发给一个配置里不存在的调用方的授权
// 停用。标过就不再改 —— 同一 id 以后重新出现也仍保持停用，只有 ActivateAuthGrant 才恢复。
// 已经孤儿或已撤销的行不动；返回真正改动的行数。
func (s *Store) OrphanAuthGrants(ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, s.unixNow())
	for _, id := range ids {
		args = append(args, id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("jobstore: begin orphan auth grants: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE auth_grants SET orphaned_at=? WHERE id IN (`+placeholders+`)
  AND orphaned_at IS NULL AND revoked_at IS NULL`, args...)
	if err != nil {
		return 0, fmt.Errorf("jobstore: orphan auth grants: %w", err)
	}
	changed, _ := res.RowsAffected()
	if changed > 0 {
		if _, err := bumpAuthGrantsVersion(tx); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("jobstore: commit orphan auth grants: %w", err)
	}
	return int(changed), nil
}

// SessionGrantAnchor 是会话登记授权要绑定的值，以及判定这条授权是否生效要看的会话事实
// （设计「接口」：POST /v1/auth/grants「会话主体自动记下 owner 与化身」；§4「生效条件」）：
//
//   - Owner / Incarnation：授权记下的就不再变，之后每次请求都要与会话**当前**的这两个值比对；
//   - State / EndedAt：会话结束（state=ended，或 ended_at 留着——接管一个已结束的终端就是
//     这种情况）后，它的授权一条也不再生效。
//
// 存储层只报事实（G022）：怎么用这些值判定是入口层的事。会话不存在时 ok 为 false、err 为 nil。
type SessionGrantAnchor struct {
	Owner       string
	Incarnation string
	State       string
	EndedAt     int64
}

// Ended reports whether the session has ended: the state says so, or the terminal stamp is still
// there (a session taken over after it ended keeps ended_at — design §4「读时还要判定 ended_at 为
// 空，作为第二道保险」).
func (a SessionGrantAnchor) Ended() bool {
	return a.State == SessionEnded || a.EndedAt != 0
}

// SessionGrantAnchor reads the facts a session grant binds to and is judged by. A session that
// does not exist answers ok=false with no error.
func (s *Store) SessionGrantAnchor(sid string) (SessionGrantAnchor, bool, error) {
	if strings.TrimSpace(sid) == "" {
		return SessionGrantAnchor{}, false, nil
	}
	var a SessionGrantAnchor
	err := s.db.QueryRow(
		`SELECT COALESCE(caller_id,''), COALESCE(incarnation,''), state, COALESCE(ended_at,0)
  FROM agent_sessions WHERE session_id = ?`, sid).
		Scan(&a.Owner, &a.Incarnation, &a.State, &a.EndedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionGrantAnchor{}, false, nil
	}
	if err != nil {
		return SessionGrantAnchor{}, false, fmt.Errorf("jobstore: session grant anchor %q: %w", sid, err)
	}
	return a, true, nil
}

// newAuthGrantID 生成授权 id：`grant-<unix>-<8 位随机十六进制>`。前缀把接口生成的 id 与种入的
// `seed:...` 区分开，一眼可认。
func newAuthGrantID() string {
	return fmt.Sprintf("grant-%d-%s", time.Now().Unix(), decisionRandomSuffix())
}

// bumpAuthGrantsVersion 递增 auth_meta.grants_version（设计「一致性」：授权表的代号）。授权表的每一条
// 写路径都在自己的事务里调它，所以代号与行永远一起落地。
func bumpAuthGrantsVersion(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`INSERT INTO auth_meta(key, value) VALUES (?, '1')
  ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(auth_meta.value AS INTEGER) + 1 AS TEXT)`,
		AuthMetaGrantsVersion); err != nil {
		return 0, fmt.Errorf("jobstore: bump auth_meta.%s: %w", AuthMetaGrantsVersion, err)
	}
	v, _, err := authMetaValue(tx, AuthMetaGrantsVersion)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jobstore: auth_meta.%s = %q is not a number", AuthMetaGrantsVersion, v)
	}
	return n, nil
}

// nullableInt 把 0 写成 NULL：auth_grants 里 expires_at / revoked_at / orphaned_at 用 NULL 表示
// 「不适用」，存 0 会让「从不过期」与「1970 年到期」看起来一样。
func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullableText 把空串写成 NULL（session_owner / session_incarnation / note 只在有值时写）。
func nullableText(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// AuthSeedVersion 返回 auth_meta.seed_version：这个数据库已经为 `users` 种到的最高种入版本，
// 从未种过时为 0。
func (s *Store) AuthSeedVersion() (int64, error) {
	return s.authMetaInt(AuthMetaSeedVersion)
}

// AuthGrantsVersion 返回 auth_meta.grants_version：授权表的代号，入口层的缓存据此失效（设计
// 「一致性」）。没有这一行时读到 0（本版本打开的库一定有此行）。
func (s *Store) AuthGrantsVersion() (int64, error) {
	return s.authMetaInt(AuthMetaGrantsVersion)
}

// authMetaInt 读一个整数值的 auth_meta 键；键不存在读作 0。
func (s *Store) authMetaInt(key string) (int64, error) {
	v, ok, err := authMetaValue(s.db, key)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jobstore: auth_meta.%s = %q is not a number", key, v)
	}
	return n, nil
}

// authMetaQueryer 是 auth_meta 读路径共用的握手：*sql.DB 与事务都能满足它，所以种入可以在事务里
// 读到刚建出来的表。
type authMetaQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// authMetaValue 读一个 auth_meta 键。ok 为 false 表示这个键从没写过（不是错误）。
func authMetaValue(q authMetaQueryer, key string) (string, bool, error) {
	var v string
	err := q.QueryRow(`SELECT value FROM auth_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("jobstore: read auth_meta.%s: %w", key, err)
	}
	return v, true, nil
}

// ---- 审计（设计「数据层」的 auth_audit、决策 10，接口 GET /v1/auth/audit）----

// AuthAuditEntry 是 auth_audit 的一行：授权的增删、只靠会话授权通过的请求、被拒的请求（含原因）。
//
// 存储层只追加、不改不删（保留期清理除外）；何时记、记什么由入口层决定。「授权的增删」与「请求类
// 记录」的写入属于计划的 T2.4（审计与使用计数）—— 本任务只提供读路径与写入入口，好让接口与过滤
// 规则在同一个提交里完成并通过测试。
//
// At 是 unix 秒；Method / Path / Status / Reason 只对请求类记录有意义（授权增删留空）。
type AuthAuditEntry struct {
	At         int64  `json:"at"`
	Actor      string `json:"actor,omitempty"`
	Action     string `json:"action"`
	Subject    string `json:"subject,omitempty"`
	Permission string `json:"permission,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	GrantID    string `json:"grant_id,omitempty"`
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	Status     int    `json:"status,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// 审计查询的条数边界：默认与上限都在存储层，入口层不写数字。
const (
	DefaultAuthAuditLimit = 100
	MaxAuthAuditLimit     = 500
)

// AuthAuditFilter 是审计读取的过滤条件（设计「接口」：按主体、权限、授权 id 过滤）。三个字段都为空
// 时不加过滤；Limit 留 0 用 DefaultAuthAuditLimit，超过 MaxAuthAuditLimit 时取上限。
type AuthAuditFilter struct {
	Subject    string
	Permission string
	GrantID    string
	Limit      int
}

// InsertAuthAudit 追加一条审计记录。存储层不判断该不该记，只保证行与字段落地。
func (s *Store) InsertAuthAudit(e AuthAuditEntry) error {
	if strings.TrimSpace(e.Action) == "" {
		return errors.New("jobstore: insert auth audit: empty action")
	}
	if e.At == 0 {
		e.At = s.unixNow()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO auth_audit
  (at, actor, action, subject, permission, session_id, grant_id, method, path, status, reason)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.At, e.Actor, e.Action, e.Subject, e.Permission, e.SessionID, e.GrantID,
		e.Method, e.Path, e.Status, e.Reason); err != nil {
		return fmt.Errorf("jobstore: insert auth audit: %w", err)
	}
	return nil
}

// MarkAuthGrantUsed atomically updates the usage counters of a live grant.
func (s *Store) MarkAuthGrantUsed(id string, at int64) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("jobstore: mark auth grant used: empty id")
	}
	if at == 0 {
		at = s.unixNow()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE auth_grants SET last_used_at=?, use_count=use_count+1 WHERE id=?`, at, id); err != nil {
		return fmt.Errorf("jobstore: mark auth grant used: %w", err)
	}
	return nil
}

// PruneAuthAudit removes entries older than the design's 90-day retention window.
func (s *Store) PruneAuthAudit(now int64) error {
	if now == 0 {
		now = s.unixNow()
	}
	cutoff := now - int64((90*24*time.Hour)/time.Second)
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`DELETE FROM auth_audit WHERE at < ?`, cutoff); err != nil {
		return fmt.Errorf("jobstore: prune auth audit: %w", err)
	}
	return nil
}

// ListAuthAudit 读审计记录，最新的在前（同一秒内按写入顺序倒序）。
func (s *Store) ListAuthAudit(f AuthAuditFilter) ([]AuthAuditEntry, error) {
	q := `SELECT at, COALESCE(actor,''), action, COALESCE(subject,''), COALESCE(permission,''),
  COALESCE(session_id,''), COALESCE(grant_id,''), COALESCE(method,''), COALESCE(path,''),
  COALESCE(status,0), COALESCE(reason,'') FROM auth_audit`
	var where []string
	var args []any
	if subject := strings.TrimSpace(f.Subject); subject != "" {
		where = append(where, "subject = ?")
		args = append(args, subject)
	}
	if permission := strings.TrimSpace(f.Permission); permission != "" {
		where = append(where, "permission = ?")
		args = append(args, permission)
	}
	if grantID := strings.TrimSpace(f.GrantID); grantID != "" {
		where = append(where, "grant_id = ?")
		args = append(args, grantID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultAuthAuditLimit
	}
	if limit > MaxAuthAuditLimit {
		limit = MaxAuthAuditLimit
	}
	args = append(args, limit)
	rows, err := s.db.Query(q+` ORDER BY at DESC, rowid DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list auth audit: %w", err)
	}
	defer rows.Close()
	var out []AuthAuditEntry
	for rows.Next() {
		var e AuthAuditEntry
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &e.Subject, &e.Permission,
			&e.SessionID, &e.GrantID, &e.Method, &e.Path, &e.Status, &e.Reason); err != nil {
			return nil, fmt.Errorf("jobstore: scan auth audit: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list auth audit: %w", err)
	}
	return out, nil
}
