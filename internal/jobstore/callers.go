package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 配置入库 P1 的两张表（设计 docs/design/2026-11-03-config-into-db-design.md §架构、
// 计划 docs/plans/2026-11-03-caller-store-p1-plan.md T1）：
//
//   - auth_callers 是「用户」（一个 token 对应的身份）的权威存储。token 只存十六进制
//     sha256，明文只在创建 / 轮换的响应里出现一次；停用与「被文件条目接管」各有一列
//     时间戳，行只增不删（id 永不复用）。
//   - config_audit 是配置与用户写入的追加日志（谁、何时、哪一项、打码后的前后值），与
//     写入同事务，按 90 天保留期清理。
//
// 建表语句在 init() 里追加到基础 schema（G057）：两张都是新表，没有旧库要 ALTER，所以
// 这批列一次给全 —— 它们也是后续只读预检的契约，之后只增不改。

var callersSchema = []string{
	`CREATE TABLE IF NOT EXISTS auth_callers (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL DEFAULT '',
  token_hash    TEXT NOT NULL DEFAULT '',
  token_hint    TEXT NOT NULL DEFAULT '',
  limits_json   TEXT NOT NULL DEFAULT '',
  ceiling_yaml  TEXT NOT NULL DEFAULT '',
  disabled_at   INTEGER,
  superseded_at INTEGER,
  created_by    TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL DEFAULT 0,
  rotated_at    INTEGER,
  updated_at    INTEGER NOT NULL DEFAULT 0
)`,
	// token_hash 故意不设唯一约束：文件里写同一个 token 给两个用户今天是合法配置，
	// 迁移前报告重复即可，不能因为约束让库升不上去。
	`CREATE INDEX IF NOT EXISTS idx_auth_callers_token_hash ON auth_callers(token_hash)`,
	// 审计按时间范围查（保留期清理）与按路径前缀查（「这一项改过什么」）。
	`CREATE TABLE IF NOT EXISTS config_audit (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  actor         TEXT NOT NULL DEFAULT '',
  path          TEXT NOT NULL DEFAULT '',
  before_masked TEXT NOT NULL DEFAULT '',
  after_masked  TEXT NOT NULL DEFAULT '',
  at            INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE INDEX IF NOT EXISTS idx_config_audit_at ON config_audit(at)`,
}

func init() { schemaStmts = append(schemaStmts, callersSchema...) }

// StoredCaller is one auth_callers row, verbatim: ceiling_yaml and limits_json are NOT
// parsed here (the config layer owns those types — G022: this layer stores facts, the
// business layer interprets them).
type StoredCaller struct {
	ID string
	// Name is the optional display name. Display only: the id stays the identity.
	Name string
	// TokenHash is the hex sha256 of the caller's token; the plaintext is never stored.
	TokenHash string
	// TokenHint is `gct_…` plus the last four characters, for a list that can tell
	// tokens apart without revealing them.
	TokenHint string
	// LimitsJSON is the stored quota block (max_concurrent_jobs / rate_limit / rate_burst)
	// as JSON; empty means "not set".
	LimitsJSON string
	// CeilingYAML is the stored ceiling block as YAML; empty means "no ceiling".
	CeilingYAML string
	// DisabledAt is when the caller was disabled (0 = enabled). A disabled caller stays in
	// the config snapshot (its ceiling keeps bounding the jobs it started) but cannot
	// authenticate.
	DisabledAt int64
	// SupersededAt is when a same-id entry appeared in the configuration FILE: the file
	// wins, so this row is hidden, disabled and never comes back without a rotation.
	SupersededAt int64
	// CreatedBy is the actor of the creation (a caller id, or `offline:<user>`).
	CreatedBy string
	CreatedAt int64
	// RotatedAt is when the token was last rotated (0 = never).
	RotatedAt int64
	UpdatedAt int64
}

const selectStoredCallerCols = `SELECT id, COALESCE(name,''), COALESCE(token_hash,''),
  COALESCE(token_hint,''), COALESCE(limits_json,''), COALESCE(ceiling_yaml,''),
  COALESCE(disabled_at,0), COALESCE(superseded_at,0), COALESCE(created_by,''),
  COALESCE(created_at,0), COALESCE(rotated_at,0), COALESCE(updated_at,0)
  FROM auth_callers`

func scanStoredCaller(sc rowScanner) (StoredCaller, error) {
	var c StoredCaller
	err := sc.Scan(&c.ID, &c.Name, &c.TokenHash, &c.TokenHint, &c.LimitsJSON, &c.CeilingYAML,
		&c.DisabledAt, &c.SupersededAt, &c.CreatedBy, &c.CreatedAt, &c.RotatedAt, &c.UpdatedAt)
	return c, err
}

// ListStoredCallers returns every auth_callers row in creation order. An EMPTY result is
// a real answer (no caller is stored); a failure must never be read as "no callers" by
// the caller (the merge treats an error as fatal — see core.MergeStoredCallers).
func (s *Store) ListStoredCallers() ([]StoredCaller, error) {
	return listStoredCallers(s.db)
}

// callerQueryer is the read seam the store and a transaction both satisfy, so the same
// list runs inside WithCallerTx.
type callerQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func listStoredCallers(q callerQueryer) ([]StoredCaller, error) {
	rows, err := q.Query(selectStoredCallerCols + ` ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list stored callers: %w", err)
	}
	defer rows.Close()
	var out []StoredCaller
	for rows.Next() {
		c, err := scanStoredCaller(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan stored caller: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list stored callers: %w", err)
	}
	return out, nil
}

// GetStoredCaller reads one row; a missing id answers ok=false with no error.
func (s *Store) GetStoredCaller(id string) (StoredCaller, bool, error) {
	return getStoredCaller(s.db, id)
}

func getStoredCaller(q callerQueryer, id string) (StoredCaller, bool, error) {
	c, err := scanStoredCaller(q.QueryRow(selectStoredCallerCols+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return StoredCaller{}, false, nil
	}
	if err != nil {
		return StoredCaller{}, false, fmt.Errorf("jobstore: get stored caller %q: %w", id, err)
	}
	return c, true, nil
}

// CallerTx is the transaction handle WithCallerTx hands to its callback: the caller-row
// writes and the audit line they imply land together, and the grant orphaning a takeover
// does happens in the same transaction (design §架构「与写入同事务」).
//
// The methods do NOT take writeMu themselves: WithCallerTx holds it for the whole
// callback, which is what keeps SQLite's single writer rule (see Store.writeMu).
type CallerTx interface {
	// ListStoredCallers re-reads every row inside the transaction, so a write followed by
	// a merge sees its own change.
	ListStoredCallers() ([]StoredCaller, error)
	// GetCaller reads one row inside the transaction; ok=false when it does not exist.
	GetCaller(id string) (StoredCaller, bool, error)
	// InsertCallerTx writes a new row.
	InsertCallerTx(c StoredCaller) error
	// UpdateCallerTx rewrites every mutable column of an existing row.
	UpdateCallerTx(c StoredCaller) error
	// OrphanGrants marks the listed grants orphaned (see orphanAuthGrantsTx).
	OrphanGrants(ids []string) (int, error)
	// ActiveGrantIDs lists the ids of the grants a subject still holds (not revoked, not
	// orphaned), so a caller that is being taken over can be orphaned by subject.
	ActiveGrantIDs(subject string) ([]string, error)
	// InsertConfigAudit appends one audit line to THIS transaction.
	InsertConfigAudit(e ConfigAuditEntry) error
	// Now is the store clock the transaction writes (unix seconds), so timestamps written
	// by one callback are ordered the way a test's pinned clock says.
	Now() int64
}

// callerTx is the CallerTx implementation over one *sql.Tx.
type callerTx struct {
	tx  *sql.Tx
	now int64
}

// WithCallerTx runs fn inside ONE write transaction under writeMu: either every write fn
// performs (rows, orphaned grants, audit lines) is committed, or none of them is. It is
// the single seam for caller writes — T3's takeover, T6's create/rotate/disable, and the
// offline repair all go through it, so "the row and its audit line are one unit" holds
// everywhere.
func (s *Store) WithCallerTx(fn func(tx CallerTx) error) error {
	if fn == nil {
		return errors.New("jobstore: with caller tx: nil callback")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("jobstore: begin caller tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(&callerTx{tx: tx, now: s.unixNow()}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("jobstore: commit caller tx: %w", err)
	}
	return nil
}

func (t *callerTx) ListStoredCallers() ([]StoredCaller, error) {
	return listStoredCallers(t.tx)
}

func (t *callerTx) GetCaller(id string) (StoredCaller, bool, error) {
	return getStoredCaller(t.tx, id)
}

func (t *callerTx) Now() int64 { return t.now }

func (t *callerTx) InsertCallerTx(c StoredCaller) error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("jobstore: insert caller: empty id")
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = t.now
	}
	if c.UpdatedAt == 0 {
		c.UpdatedAt = c.CreatedAt
	}
	if _, err := t.tx.Exec(`INSERT INTO auth_callers
  (id, name, token_hash, token_hint, limits_json, ceiling_yaml, disabled_at, superseded_at,
   created_by, created_at, rotated_at, updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.TokenHash, c.TokenHint, c.LimitsJSON, c.CeilingYAML,
		nullableInt(c.DisabledAt), nullableInt(c.SupersededAt), c.CreatedBy, c.CreatedAt,
		nullableInt(c.RotatedAt), c.UpdatedAt); err != nil {
		return fmt.Errorf("jobstore: insert caller %s: %w", c.ID, err)
	}
	return nil
}

func (t *callerTx) UpdateCallerTx(c StoredCaller) error {
	if strings.TrimSpace(c.ID) == "" {
		return errors.New("jobstore: update caller: empty id")
	}
	if c.UpdatedAt == 0 {
		c.UpdatedAt = t.now
	}
	res, err := t.tx.Exec(`UPDATE auth_callers SET name=?, token_hash=?, token_hint=?,
  limits_json=?, ceiling_yaml=?, disabled_at=?, superseded_at=?, rotated_at=?, updated_at=?
  WHERE id=?`,
		c.Name, c.TokenHash, c.TokenHint, c.LimitsJSON, c.CeilingYAML,
		nullableInt(c.DisabledAt), nullableInt(c.SupersededAt), nullableInt(c.RotatedAt),
		c.UpdatedAt, c.ID)
	if err != nil {
		return fmt.Errorf("jobstore: update caller %s: %w", c.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("jobstore: update caller %s: %w", c.ID, ErrStoredCallerNotFound)
	}
	return nil
}

func (t *callerTx) OrphanGrants(ids []string) (int, error) {
	return orphanAuthGrantsTx(t.tx, t.now, ids)
}

func (t *callerTx) ActiveGrantIDs(subject string) ([]string, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("jobstore: active grant ids: empty subject")
	}
	rows, err := t.tx.Query(
		`SELECT id FROM auth_grants WHERE subject = ? AND revoked_at IS NULL AND orphaned_at IS NULL`,
		subject)
	if err != nil {
		return nil, fmt.Errorf("jobstore: active grant ids for %q: %w", subject, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("jobstore: active grant ids for %q: %w", subject, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: active grant ids for %q: %w", subject, err)
	}
	return out, nil
}

func (t *callerTx) InsertConfigAudit(e ConfigAuditEntry) error {
	if e.At == 0 {
		e.At = t.now
	}
	return insertConfigAuditRow(t.tx, e)
}

// ErrStoredCallerNotFound reports an update of an id auth_callers does not have.
var ErrStoredCallerNotFound = errors.New("stored caller not found")

// orphanAuthGrantsTx is OrphanAuthGrants' transaction body: the exported method wraps it
// with writeMu + Begin/Commit, WithCallerTx calls it directly (it already holds both).
func orphanAuthGrantsTx(tx *sql.Tx, now int64, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, now)
	for _, id := range ids {
		args = append(args, id)
	}
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
	return int(changed), nil
}

// ---- config_audit（D8 / D12）----

// ConfigAuditEntry is one config_audit row: who changed which entry, when, and the masked
// values before and after. Secrets never reach this table (the caller masks), which is
// why there is no "raw" column to accidentally fill.
type ConfigAuditEntry struct {
	ID           int64  `json:"id,omitempty"`
	Actor        string `json:"actor"`
	Path         string `json:"path"`
	BeforeMasked string `json:"before_masked,omitempty"`
	AfterMasked  string `json:"after_masked,omitempty"`
	At           int64  `json:"at"`
}

// 审计查询的条数边界：默认与上限都在存储层，入口层不写数字。
const (
	DefaultConfigAuditLimit = 100
	MaxConfigAuditLimit     = 500
)

// configAuditExecer is the write seam the store and a transaction both satisfy.
type configAuditExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertConfigAuditRow appends one audit line; At is filled by the caller (a transaction
// has its own clock).
func insertConfigAuditRow(q configAuditExecer, e ConfigAuditEntry) error {
	if strings.TrimSpace(e.Path) == "" {
		return errors.New("jobstore: insert config audit: empty path")
	}
	if _, err := q.Exec(`INSERT INTO config_audit (actor, path, before_masked, after_masked, at)
  VALUES (?,?,?,?,?)`, e.Actor, e.Path, e.BeforeMasked, e.AfterMasked, e.At); err != nil {
		return fmt.Errorf("jobstore: insert config audit: %w", err)
	}
	return nil
}

// InsertConfigAudit appends one audit line in its own transaction. It is what a write that
// does not otherwise touch the database uses (D12: a file-source caller's ceiling edit).
func (s *Store) InsertConfigAudit(e ConfigAuditEntry) error {
	if e.At == 0 {
		e.At = s.unixNow()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return insertConfigAuditRow(s.db, e)
}

// ListConfigAudit returns the audit lines whose path has the given prefix, newest first.
// An empty prefix lists everything; limit 0 uses DefaultConfigAuditLimit.
func (s *Store) ListConfigAudit(prefix string, limit int) ([]ConfigAuditEntry, error) {
	q := `SELECT id, COALESCE(actor,''), COALESCE(path,''), COALESCE(before_masked,''),
  COALESCE(after_masked,''), COALESCE(at,0) FROM config_audit`
	var args []any
	if prefix = strings.TrimSpace(prefix); prefix != "" {
		q += ` WHERE path LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(prefix)+"%")
	}
	if limit <= 0 {
		limit = DefaultConfigAuditLimit
	}
	if limit > MaxConfigAuditLimit {
		limit = MaxConfigAuditLimit
	}
	args = append(args, limit)
	rows, err := s.db.Query(q+` ORDER BY at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list config audit: %w", err)
	}
	defer rows.Close()
	var out []ConfigAuditEntry
	for rows.Next() {
		var e ConfigAuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Path, &e.BeforeMasked, &e.AfterMasked, &e.At); err != nil {
			return nil, fmt.Errorf("jobstore: scan config audit: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list config audit: %w", err)
	}
	return out, nil
}

// escapeLike escapes the LIKE metacharacters of a literal prefix (`%`, `_`, `\`), so a
// caller id containing `_` matches itself instead of any character.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// ---- 离线修复（计划的 T1 / D5）----

// OpenExistingForRepair opens an EXISTING database for the offline repair commands without
// creating, migrating or tightening anything: it stats the path first, so a typo'd path
// produces an error instead of an empty database (the design forbids the CLI from creating
// one), and it never runs applySchema/migrate — repair works on the schema a running server
// wrote, not on one this binary would like to have. A database without auth_callers is an
// error too: repairing a caller there is meaningless.
func OpenExistingForRepair(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("jobstore: empty db path")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open existing for repair %q: %w", path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("jobstore: open existing for repair %q: is a directory", path)
	}
	dsn := fmt.Sprintf(
		"file:%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)",
		path, busyTimeoutMS,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open existing for repair %q: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("jobstore: open existing for repair %q: %w", path, err)
	}
	s := &Store{db: db, path: path, sessionMessageLogRoot: filepath.Dir(path)}
	cols, err := s.tableColumns("auth_callers")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("jobstore: open existing for repair %q: %w", path, err)
	}
	if len(cols) == 0 {
		_ = db.Close()
		return nil, fmt.Errorf("jobstore: open existing for repair %q: no auth_callers table (the database predates callers in the database)", path)
	}
	return s, nil
}

// DBPath returns the database file this store was opened on (the offline commands print
// it, so an operator can see WHICH database was repaired — D5).
func (s *Store) DBPath() string {
	if s == nil {
		return ""
	}
	return s.path
}

// RepairOptions carries the checks and the replacement value the caller supplies, so
// jobstore never imports the config package's caller types (G022). A nil check accepts
// everything.
type RepairOptions struct {
	// ValidCeiling reports whether a stored ceiling_yaml decodes into a usable ceiling.
	ValidCeiling func(string) error
	// ValidLimits reports whether a stored limits_json decodes into usable quotas.
	ValidLimits func(string) error
	// ValidName reports whether a stored display name is legal.
	ValidName func(string) error
	// LockedCeilingYAML replaces an unusable ceiling_yaml. It is the caller's most
	// restrictive ceiling (an unset ceiling would UNLIMIT the caller, which is the wrong
	// direction for a repair).
	LockedCeilingYAML string
	// At is the audit/clock stamp; 0 means the store clock.
	At int64
}

// RepairResult reports what RepairCaller changed, so the offline command can print it. The
// before/after fields are the FIELD NAMES that changed — never the values, because a repair
// prints on a terminal and the token hash must not travel.
type RepairResult struct {
	ID        string   `json:"id"`
	Found     bool     `json:"found"`
	Changed   []string `json:"changed,omitempty"`
	Disabled  bool     `json:"disabled,omitempty"`
	LimitJSON string   `json:"-"`
}

// RepairCaller makes ONE stored caller more restrictive so the server can start again. It
// only ever repairs toward the safe side, and it never deletes a row (the id must not be
// reused) and never touches the token hash:
//
//   - an enabled row whose fields fail the caller's checks is DISABLED (the row stays, so a
//     later enable after a rotation reuses the same id);
//   - an unusable ceiling_yaml becomes opts.LockedCeilingYAML;
//   - an unusable limits_json is cleared (no quota);
//   - an unusable name is cleared (the name is display-only).
//
// The row and its config_audit line (actor passed in, path `server.callers.<id>`) land in
// one transaction.
func (s *Store) RepairCaller(id, actor string, opts RepairOptions) (RepairResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return RepairResult{}, errors.New("jobstore: repair caller: empty id")
	}
	res := RepairResult{ID: id}
	err := s.WithCallerTx(func(tx CallerTx) error {
		c, ok, err := tx.GetCaller(id)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		res.Found = true
		now := opts.At
		if now == 0 {
			now = tx.Now()
		}
		before := describeStoredCaller(c)
		if opts.ValidName != nil && strings.TrimSpace(c.Name) != "" {
			if err := opts.ValidName(c.Name); err != nil {
				c.Name = ""
				res.Changed = append(res.Changed, "name")
			}
		}
		if opts.ValidCeiling != nil && strings.TrimSpace(c.CeilingYAML) != "" {
			if err := opts.ValidCeiling(c.CeilingYAML); err != nil {
				c.CeilingYAML = opts.LockedCeilingYAML
				res.Changed = append(res.Changed, "ceiling")
			}
		}
		if opts.ValidLimits != nil && strings.TrimSpace(c.LimitsJSON) != "" {
			if err := opts.ValidLimits(c.LimitsJSON); err != nil {
				c.LimitsJSON = ""
				res.Changed = append(res.Changed, "limits")
			}
		}
		if c.DisabledAt == 0 && len(res.Changed) > 0 {
			c.DisabledAt = now
			res.Changed = append(res.Changed, "disabled")
		}
		if len(res.Changed) == 0 {
			return nil
		}
		c.UpdatedAt = now
		if err := tx.UpdateCallerTx(c); err != nil {
			return err
		}
		res.Disabled = c.DisabledAt != 0
		res.LimitJSON = c.LimitsJSON
		return tx.InsertConfigAudit(ConfigAuditEntry{
			At: now, Actor: actor, Path: "server.callers." + id,
			BeforeMasked: before, AfterMasked: describeStoredCaller(c),
		})
	})
	return res, err
}

// describeStoredCaller renders the audited picture of a row WITHOUT its token hash: the
// audit line, the terminal and the logs must never carry the hash or anything derived from
// it beyond the hint that is meant to be shown.
func describeStoredCaller(c StoredCaller) string {
	return fmt.Sprintf("name=%q hint=%q ceiling_yaml=%q limits_json=%q disabled_at=%d superseded_at=%d",
		c.Name, c.TokenHint, c.CeilingYAML, c.LimitsJSON, c.DisabledAt, c.SupersededAt)
}

// ConfigAuditRetention is how long config_audit rows are kept (D8: 90 days, the same
// window auth_audit uses).
const ConfigAuditRetention = 90 * 24 * time.Hour
