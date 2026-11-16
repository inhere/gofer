package jobstore

import (
	"fmt"
	"strings"
)

// admin_audit 是管理类（写）请求的追加日志：谁、何时、调了哪条路由、对哪个资源、结果如何。
// 只记元数据：不存请求体与查询值（可能带秘密），查询只留键名。被授权拒绝的请求也记。
// 与 auth_audit（授权判定）、config_audit（调用方变更）并存，互不合并。
//
// 新表（IF NOT EXISTS 追加到基础 schema，G057：没有旧库要 ALTER，语句也不引用迁移才有的列）。
var adminAuditSchema = []string{
	`CREATE TABLE IF NOT EXISTS admin_audit (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  at          INTEGER NOT NULL DEFAULT 0,
  caller_id   TEXT NOT NULL DEFAULT '',
  caller_kind TEXT NOT NULL DEFAULT '',
  session_id  TEXT NOT NULL DEFAULT '',
  job_id      TEXT NOT NULL DEFAULT '',
  method      TEXT NOT NULL DEFAULT '',
  route       TEXT NOT NULL DEFAULT '',
  target      TEXT NOT NULL DEFAULT '',
  query_keys  TEXT NOT NULL DEFAULT '',
  status      INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  ip          TEXT NOT NULL DEFAULT '',
  error       TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_admin_audit_at ON admin_audit(at)`,
	`CREATE INDEX IF NOT EXISTS idx_admin_audit_caller ON admin_audit(caller_id, at)`,
}

func init() { schemaStmts = append(schemaStmts, adminAuditSchema...) }

// AdminAuditEntry 是 admin_audit 的一行。At 是 unix 秒；Route 是路由模板（如
// `POST /v1/schedules/{id}/run`）；Target 是路径参数拼成的 `name=value`；QueryKeys 是查询键名
// （逗号分隔、已排序），绝不含值。
type AdminAuditEntry struct {
	ID         int64  `json:"id"`
	At         int64  `json:"at"`
	CallerID   string `json:"caller_id,omitempty"`
	CallerKind string `json:"caller_kind,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	Method     string `json:"method"`
	Route      string `json:"route"`
	Target     string `json:"target,omitempty"`
	QueryKeys  string `json:"query_keys,omitempty"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	IP         string `json:"ip,omitempty"`
	Error      string `json:"error,omitempty"`
}

// 审计查询的条数边界：默认与上限都在存储层，入口层不写数字。
const (
	DefaultAdminAuditLimit = 100
	MaxAdminAuditLimit     = 500
)

// AdminAuditFilter 是 admin_audit 读取的过滤条件，字段留空即不过滤。
//
// Route 是路由模板的子串匹配；Status 为 "ok"（< 400）或 "fail"（>= 400）；Since / Until 是
// unix 秒（含两端）；Cursor 是上一页最后一行的 id（只返回 id 更小的行）。
type AdminAuditFilter struct {
	Caller string
	Route  string
	Status string
	Since  int64
	Until  int64
	Cursor int64
	Limit  int
}

// InsertAdminAuditBatch 在一个事务里追加一批审计行（At 为 0 时补本库时钟）。空批什么都不做。
func (s *Store) InsertAdminAuditBatch(entries []AdminAuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	now := s.unixNow()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin admin audit batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.Prepare(`INSERT INTO admin_audit
  (at, caller_id, caller_kind, session_id, job_id, method, route, target, query_keys, status, duration_ms, ip, error)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("jobstore: prepare admin audit: %w", err)
	}
	defer stmt.Close()
	for _, e := range entries {
		if e.At == 0 {
			e.At = now
		}
		if _, err := stmt.Exec(e.At, e.CallerID, e.CallerKind, e.SessionID, e.JobID, e.Method, e.Route,
			e.Target, e.QueryKeys, e.Status, e.DurationMs, e.IP, e.Error); err != nil {
			return fmt.Errorf("jobstore: insert admin audit: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: commit admin audit batch: %w", err)
	}
	return nil
}

// ListAdminAudit 读审计记录，最新的在前。返回的 next 是下一页的游标（0 = 没有更多）。
func (s *Store) ListAdminAudit(f AdminAuditFilter) (entries []AdminAuditEntry, next int64, err error) {
	q := `SELECT id, at, caller_id, caller_kind, session_id, job_id, method, route, target,
  query_keys, status, duration_ms, ip, error FROM admin_audit`
	var where []string
	var args []any
	if v := strings.TrimSpace(f.Caller); v != "" {
		where = append(where, "caller_id = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(f.Route); v != "" {
		where = append(where, `route LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(v)+"%")
	}
	switch strings.ToLower(strings.TrimSpace(f.Status)) {
	case "ok":
		where = append(where, "status < 400")
	case "fail":
		where = append(where, "status >= 400")
	case "":
	default:
		return nil, 0, fmt.Errorf("jobstore: list admin audit: status must be ok or fail, got %q", f.Status)
	}
	if f.Since > 0 {
		where = append(where, "at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		where = append(where, "at <= ?")
		args = append(args, f.Until)
	}
	if f.Cursor > 0 {
		where = append(where, "id < ?")
		args = append(args, f.Cursor)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultAdminAuditLimit
	}
	if limit > MaxAdminAuditLimit {
		limit = MaxAdminAuditLimit
	}
	// One extra row tells whether another page exists.
	args = append(args, limit+1)
	rows, err := s.db.Query(q+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("jobstore: list admin audit: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e AdminAuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.CallerID, &e.CallerKind, &e.SessionID, &e.JobID, &e.Method,
			&e.Route, &e.Target, &e.QueryKeys, &e.Status, &e.DurationMs, &e.IP, &e.Error); err != nil {
			return nil, 0, fmt.Errorf("jobstore: scan admin audit: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("jobstore: list admin audit: %w", err)
	}
	if len(entries) > limit {
		entries = entries[:limit]
		next = entries[limit-1].ID
	}
	return entries, next, nil
}

// PruneAuditBefore 删除早于 cutoff（unix 秒）的 admin_audit 与 auth_audit 行，返回各自删除数。
// 保留期由入口层按 server.audit_retention_days 算出 cutoff；这里不写天数。
func (s *Store) PruneAuditBefore(cutoff int64) (admin, auth int64, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM admin_audit WHERE at < ?`, cutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("jobstore: prune admin audit: %w", err)
	}
	admin, _ = res.RowsAffected()
	res, err = s.db.Exec(`DELETE FROM auth_audit WHERE at < ?`, cutoff)
	if err != nil {
		return admin, 0, fmt.Errorf("jobstore: prune auth audit: %w", err)
	}
	auth, _ = res.RowsAffected()
	return admin, auth, nil
}
