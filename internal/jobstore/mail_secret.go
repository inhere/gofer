package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 保密 (design 2026-10-16-session-topics-mailbox-and-inbox-design.md 「安全、数据、运维与回滚」的
// 「敏感信息」): the secret scan and the redaction reach the mailbox. A letter is scanned and
// redacted ONE letter at a time here — the per-job surface (secret_scan.go / redact.go) already
// covers every letter that carries a job_id, because such a row is one of that job's text rows
// (forEachJobDBText → jobRowBelongs).
//
// Two properties are shared with the job surface and must stay that way: the report is
// aggregate-only (no body, no matched text, no re-echoed literal) and the redaction writes an
// exact count nothing can read back.

// ErrMailNotFound is "there is no letter with that id" — a redaction of a letter that vanished
// between the permission check and the write (the mailbox's own send / reply paths report the
// absence themselves).
var ErrMailNotFound = errors.New("jobstore: mail not found")

// SecretScanMail is one letter a scan matched: which letter, in which conversation thread, its
// two ends as display labels, its state, the project at either end (when one has one) and where
// the matches are. Like SecretScanJob it never carries the letter's body or the matched text.
type SecretScanMail struct {
	MailID    int64                `json:"mail_id"`
	ThreadID  string               `json:"thread_id,omitempty"`
	From      string               `json:"from,omitempty"`
	To        string               `json:"to,omitempty"`
	State     string               `json:"state"`
	Project   string               `json:"project,omitempty"`
	Locations []SecretScanLocation `json:"locations"`
}

// SecretBatchMailRedact is one redacted letter inside a batch redaction's report.
type SecretBatchMailRedact struct {
	MailID int64        `json:"mail_id"`
	Report RedactReport `json:"report"`
}

// MailPartyLabel is the human label of one end of a letter: 「会话 sess-a（调用方 alice）」 /
// 「job job-7（调用方 alice）」 / 「调用方 alice」. The wording is internal/mail's Origin.Display
// (internal/mail/origin.go); the store cannot import the mailbox (G022), so the wording lives
// here and a mailbox test keeps the two in step. An unknown kind (an address that resolved to
// nobody) yields "" — the caller shows the raw address instead.
func MailPartyLabel(kind, id, caller string) string {
	caller = strings.TrimSpace(caller)
	switch strings.TrimSpace(kind) {
	case MailKindSession:
		return mailWithCaller("会话 "+strings.TrimSpace(id), caller)
	case MailKindJob:
		return mailWithCaller("job "+strings.TrimSpace(id), caller)
	case MailKindCaller:
		if caller == "" {
			return "调用方"
		}
		return "调用方 " + caller
	}
	return ""
}

func mailWithCaller(label, caller string) string {
	if caller == "" {
		return label
	}
	return label + "（调用方 " + caller + "）"
}

// mailScanTexts are the letter columns a scan reads and a redaction rewrites: the free text a
// person wrote. The identifying columns (ids, addresses, state, timestamps) are not prose, and
// rewriting them would break the letter's routing rather than erase a secret.
var mailScanTexts = []string{"body", "last_error"}

// scanMailSecrets reports the letters matching literals / patterns. The filter is the SDK's, not
// a policy: OwnerID restricts the answer to the caller's own letters — its from_caller, or the
// caller that owns the recipient (a session's caller_id / a job's caller_id, and to_caller for a
// letter addressed to a caller, which is what the mailbox stamps), the same two sides mayTouchMail
// judges in the entry layer — while ProjectKey keeps the letters whose recipient or sender
// session / job is in that project and SinceUnix the letters created at or after it.
func (s *Store) scanMailSecrets(literals []string, compiled []*regexp.Regexp, filter SecretScanFilter) ([]SecretScanMail, error) {
	where := make([]string, 0, 3)
	args := make([]any, 0, 6)
	if key := strings.TrimSpace(filter.ProjectKey); key != "" {
		where = append(where, `(COALESCE(ts.project_key,'')=? OR COALESCE(tj.project_key,'')=?`+
			` OR COALESCE(fs.project_key,'')=? OR COALESCE(fj.project_key,'')=?)`)
		args = append(args, key, key, key, key)
	}
	if filter.SinceUnix > 0 {
		where = append(where, "m.created_at>=?")
		args = append(args, filter.SinceUnix)
	}
	if owner := strings.TrimSpace(filter.OwnerID); owner != "" {
		where = append(where, `(COALESCE(m.from_caller,'')=? OR COALESCE(m.to_caller,'')=?`+
			` OR COALESCE(ts.caller_id,'')=? OR COALESCE(tj.caller_id,'')=?)`)
		args = append(args, owner, owner, owner, owner)
	}
	// The project of an end is the session's or the job's — the same resolution the entry layer
	// applies to a single letter (httpapi's mailEndProject), written as joins so one query does
	// the whole scan.
	q := `SELECT m.id, COALESCE(m.thread_id,''), m.from_kind, COALESCE(m.from_id,''),
	  COALESCE(m.from_caller,''), COALESCE(m.to_kind,''), COALESCE(m.to_id,''), COALESCE(m.to_caller,''),
	  COALESCE(m.to_address,''), m.state, COALESCE(m.body,''), COALESCE(m.last_error,''),
	  COALESCE(ts.project_key,''), COALESCE(tj.project_key,''), COALESCE(fs.project_key,''), COALESCE(fj.project_key,'')
	  FROM mail m
	  LEFT JOIN agent_sessions ts ON m.to_kind='` + MailKindSession + `' AND ts.session_id=m.to_id
	  LEFT JOIN jobs tj ON m.to_kind='` + MailKindJob + `' AND tj.id=m.to_id
	  LEFT JOIN agent_sessions fs ON m.from_kind='` + MailKindSession + `' AND fs.session_id=m.from_id
	  LEFT JOIN jobs fj ON m.from_kind='` + MailKindJob + `' AND fj.id=m.from_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY m.id"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobstore: secret scan mail: %w", err)
	}
	defer rows.Close()
	var out []SecretScanMail
	for rows.Next() {
		var (
			id                                                                   int64
			thread, fromKind, fromID, fromCaller, toKind, toID, toCaller, toAddr string
			state, body, lastError                                               string
			toSessionProject, toJobProject, fromSessionProject, fromJobProject   string
		)
		if err := rows.Scan(&id, &thread, &fromKind, &fromID, &fromCaller, &toKind, &toID, &toCaller,
			&toAddr, &state, &body, &lastError,
			&toSessionProject, &toJobProject, &fromSessionProject, &fromJobProject); err != nil {
			return nil, fmt.Errorf("jobstore: secret scan mail row: %w", err)
		}
		locations := make([]SecretScanLocation, 0, len(mailScanTexts))
		for _, pair := range []struct{ column, value string }{{"body", body}, {"last_error", lastError}} {
			count := secretMatchCount(pair.value, literals, compiled)
			if count == 0 {
				continue
			}
			locations = append(locations, SecretScanLocation{
				Position: safeScanLabel("db:mail."+pair.column, literals, compiled), Matches: count,
			})
		}
		if len(locations) == 0 {
			continue
		}
		to := MailPartyLabel(toKind, toID, toCaller)
		if to == "" {
			to = toAddr
		}
		out = append(out, SecretScanMail{
			MailID: id, ThreadID: thread, From: MailPartyLabel(fromKind, fromID, fromCaller), To: to,
			State: state, Project: firstNonEmptyMailProject(toSessionProject, toJobProject, fromSessionProject, fromJobProject),
			Locations: locations,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: secret scan mail rows: %w", err)
	}
	return out, nil
}

// firstNonEmptyMailProject picks the project a letter is filed under: the recipient's first, then
// the sender's.
func firstNonEmptyMailProject(candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

// RedactMail replaces literals and RE2 patterns in one letter — its body and its hold reason — and
// in the capture-box summaries that quote it by id (the item's text and its journal lines), then
// checkpoints the WAL. The report is aggregate-only, and the letter's identifying columns are
// never touched.
//
// ANY state is allowed, unlike job redaction's terminal-only rule: a letter's body is final text
// (no delivery path rewrites it), so a secret can and must be removable from a letter that is
// still on its way, not only from one that was read.
func (s *Store) RedactMail(id int64, literals, patterns []string) (RedactReport, error) {
	return s.redactMail(id, literals, patterns, true)
}

func (s *Store) redactMail(id int64, literals, patterns []string, purge bool) (RedactReport, error) {
	var report RedactReport
	if id <= 0 {
		return report, fmt.Errorf("%w: invalid mail id %d", ErrMailInvalid, id)
	}
	compiled, err := compileRedactPatterns(literals, patterns)
	if err != nil {
		return report, err
	}
	defer s.emit(Change{Kind: ChangeMail, ID: mailIDString(id)})
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok, err := s.mailLocked(id); err != nil {
		return report, err
	} else if !ok {
		return report, fmt.Errorf("%w: %d", ErrMailNotFound, id)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return report, fmt.Errorf("jobstore: redact mail: begin: %w", err)
	}
	if err := redactMailText(tx, id, literals, compiled, &report); err != nil {
		_ = tx.Rollback()
		return RedactReport{}, err
	}
	if err := redactMailReferences(tx, id, literals, compiled, &report); err != nil {
		_ = tx.Rollback()
		return RedactReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return RedactReport{}, fmt.Errorf("jobstore: redact mail: commit: %w", err)
	}
	if purge {
		s.purgeWAL()
	}
	return report, nil
}

// redactMailText rewrites the letter's own free text inside tx.
func redactMailText(tx *sql.Tx, id int64, literals []string, patterns []*regexp.Regexp, report *RedactReport) error {
	for _, column := range mailScanTexts {
		var value string
		if err := tx.QueryRow(`SELECT COALESCE(`+column+`,'') FROM mail WHERE id=?`, id).Scan(&value); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %d", ErrMailNotFound, id)
			}
			return fmt.Errorf("jobstore: redact mail: read %s: %w", column, err)
		}
		replaced, count := redactString(value, literals, patterns)
		if count == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE mail SET `+column+`=? WHERE id=?`, replaced, id); err != nil {
			return fmt.Errorf("jobstore: redact mail: update %s: %w", column, err)
		}
		report.DBMatches += count
	}
	return nil
}

// mailReferenceSources are the collection-box texts that quote a letter by id (设计「敏感信息」:
// 条目只存信件引用与摘要) — the item's own text (「邮件 #12 待指派 … 正文摘录：…」, written by
// internal/mail's CaptureText) and its journal lines (「信件 #12 收到回复：…」, internal/work).
// Both are summaries OF the letter, so a redacted letter must take its head-of-body preview and
// its reply summary with it.
var mailReferenceSources = []struct{ table, column string }{
	{"work_items", "title"},
	{"work_journal", "text"},
}

// mailRefPattern matches a reference to one letter by id: "#12" not glued to further digits (so
// "#120" is another letter).
func mailRefPattern(id int64) *regexp.Regexp {
	return regexp.MustCompile(`#` + strconv.FormatInt(id, 10) + `(?:[^0-9]|$)`)
}

// redactMailReferences rewrites every summary that quotes letter id inside tx. It redacts the
// text it finds (the line / item keeps its wording and its letter id, only the secret goes), and
// only touches rows whose text actually holds a match.
func redactMailReferences(tx *sql.Tx, id int64, literals []string, patterns []*regexp.Regexp, report *RedactReport) error {
	ref := mailRefPattern(id)
	needle := "%#" + strconv.FormatInt(id, 10) + "%"
	for _, src := range mailReferenceSources {
		rows, err := tx.Query(`SELECT rowid, `+quoteIdent(src.column)+` FROM `+quoteIdent(src.table)+
			` WHERE `+quoteIdent(src.column)+` LIKE ?`, needle)
		if err != nil {
			return fmt.Errorf("jobstore: redact mail: read %s.%s: %w", src.table, src.column, err)
		}
		type refRow struct {
			rowid int64
			text  string
		}
		var hits []refRow
		for rows.Next() {
			var r refRow
			if err := rows.Scan(&r.rowid, &r.text); err != nil {
				rows.Close()
				return fmt.Errorf("jobstore: redact mail: scan %s.%s: %w", src.table, src.column, err)
			}
			if ref.MatchString(r.text) {
				hits = append(hits, r)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("jobstore: redact mail: scan %s.%s: %w", src.table, src.column, err)
		}
		rows.Close()
		for _, r := range hits {
			replaced, count := redactString(r.text, literals, patterns)
			if count == 0 {
				continue
			}
			if _, err := tx.Exec(`UPDATE `+quoteIdent(src.table)+` SET `+quoteIdent(src.column)+
				`=? WHERE rowid=?`, replaced, r.rowid); err != nil {
				return fmt.Errorf("jobstore: redact mail: update %s.%s: %w", src.table, src.column, err)
			}
			report.DBMatches += count
		}
	}
	return nil
}

// redactMailSummariesOfJob rewrites the collection-box summaries of every letter that belongs to
// jobID. The job's own text pass already rewrote such a letter's BODY (a mail row carrying job_id
// is one of the job's rows, forEachJobDBText), but the summaries live under the work item, not
// under the job — so `job redact` has to reach them here.
func redactMailSummariesOfJob(tx *sql.Tx, jobID string, literals []string, patterns []*regexp.Regexp, report *RedactReport) error {
	rows, err := tx.Query(`SELECT id FROM mail WHERE COALESCE(job_id,'')=? ORDER BY id`, strings.TrimSpace(jobID))
	if err != nil {
		return fmt.Errorf("jobstore: redact: read job mail: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("jobstore: redact: scan job mail: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("jobstore: redact: scan job mail: %w", err)
	}
	rows.Close()
	for _, id := range ids {
		if err := redactMailReferences(tx, id, literals, patterns, report); err != nil {
			return err
		}
	}
	return nil
}
