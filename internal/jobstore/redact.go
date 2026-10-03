package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const redactReplacement = "***REDACTED***"

// RedactReport contains only aggregate counts and skipped relative paths. It
// deliberately has no field that can carry a literal or a matched value.
type RedactReport struct {
	DBMatches    int
	FileMatches  int
	SkippedFiles []string
}

func (r RedactReport) String() string {
	return fmt.Sprintf("db_matches=%d file_matches=%d skipped_files=%d", r.DBMatches, r.FileMatches, len(r.SkippedFiles))
}

// RedactJob replaces literals and RE2 patterns in every text column belonging
// to one terminal job, then scans its result directory. DB changes and the
// audit event are committed together; files are rewritten with temp-file swaps.
func (s *Store) RedactJob(jobID string, literals, patterns []string) (RedactReport, error) {
	var report RedactReport
	if strings.TrimSpace(jobID) == "" {
		return report, errors.New("jobstore: redact: empty job id")
	}
	if len(literals) == 0 && len(patterns) == 0 {
		return report, errors.New("jobstore: redact: at least one literal or pattern is required")
	}
	for _, literal := range literals {
		if literal == "" {
			return report, errors.New("jobstore: redact: empty literal")
		}
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern == "" {
			return report, errors.New("jobstore: redact: empty pattern")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return report, fmt.Errorf("jobstore: redact: invalid pattern: %w", err)
		}
		compiled = append(compiled, re)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var status, resultDir string
	if err := s.db.QueryRow(`SELECT status, COALESCE(result_dir,'') FROM jobs WHERE id = ?`, jobID).Scan(&status, &resultDir); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return report, fmt.Errorf("jobstore: redact: job %q not found", jobID)
		}
		return report, fmt.Errorf("jobstore: redact: read job: %w", err)
	}
	if !isTerminalJobStatus(status) {
		return report, fmt.Errorf("jobstore: redact: job %q is not terminal (status %s)", jobID, status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return report, fmt.Errorf("jobstore: redact: begin: %w", err)
	}
	if err := redactJobTables(tx, jobID, literals, compiled, &report); err != nil {
		_ = tx.Rollback()
		return RedactReport{}, err
	}
	detail, _ := json.Marshal(map[string]any{
		"db_matches":         report.DBMatches,
		"file_matches":       report.FileMatches,
		"skipped_file_count": len(report.SkippedFiles),
		"literal_count":      len(literals),
		"pattern_count":      len(patterns),
	})
	if _, err := tx.Exec(`INSERT INTO job_events (job_id, type, detail_json, at) VALUES (?,?,?,strftime('%s','now'))`, jobID, "job.redacted", string(detail)); err != nil {
		_ = tx.Rollback()
		return RedactReport{}, fmt.Errorf("jobstore: redact: audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RedactReport{}, fmt.Errorf("jobstore: redact: commit: %w", err)
	}
	if resultDir != "" {
		if err := redactResultDir(resultDir, literals, compiled, &report); err != nil {
			if !os.IsNotExist(err) {
				return report, err
			}
		}
	}
	sort.Strings(report.SkippedFiles)
	return report, nil
}

func isTerminalJobStatus(status string) bool {
	switch status {
	case "done", "failed", "cancelled", "timeout", "rejected":
		return true
	default:
		return false
	}
}

type tableColumn struct {
	name string
	text bool
}

func redactJobTables(tx *sql.Tx, jobID string, literals []string, patterns []*regexp.Regexp, report *RedactReport) error {
	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("jobstore: redact: list tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("jobstore: redact: scan table: %w", err)
		}
		if err := redactOneTable(tx, table, jobID, literals, patterns, report); err != nil {
			return err
		}
	}
	return rows.Err()
}

func redactOneTable(tx *sql.Tx, table, jobID string, literals []string, patterns []*regexp.Regexp, report *RedactReport) error {
	ident := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	info, err := tx.Query(`PRAGMA table_info(` + ident + `)`)
	if err != nil {
		return fmt.Errorf("jobstore: redact: table %s columns: %w", table, err)
	}
	var cols []tableColumn
	for info.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := info.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			info.Close()
			return fmt.Errorf("jobstore: redact: table %s column: %w", table, err)
		}
		cols = append(cols, tableColumn{name: name, text: strings.EqualFold(typ, "TEXT") || typ == ""})
	}
	info.Close()
	if len(cols) == 0 {
		return nil
	}
	query := `SELECT rowid, * FROM ` + ident
	rows, err := tx.Query(query)
	if err != nil {
		return nil // WITHOUT ROWID/system tables are not job content owners.
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, len(cols)+1)
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("jobstore: redact: table %s row: %w", table, err)
		}
		if !jobRowBelongs(table, cols, values[1:], jobID) {
			continue
		}
		rowid, ok := values[0].(int64)
		if !ok {
			continue
		}
		for i, col := range cols {
			if !col.text {
				continue
			}
			text, ok := values[i+1].(string)
			if !ok || text == "" {
				continue
			}
			replaced, count := redactString(text, literals, patterns)
			if count == 0 {
				continue
			}
			if _, err := tx.Exec(`UPDATE `+ident+` SET `+quoteIdent(col.name)+`=? WHERE rowid=?`, replaced, rowid); err != nil {
				return fmt.Errorf("jobstore: redact: update %s.%s: %w", table, col.name, err)
			}
			report.DBMatches += count
		}
	}
	return rows.Err()
}

func jobRowBelongs(table string, cols []tableColumn, values []any, jobID string) bool {
	if table == "jobs" {
		for i, col := range cols {
			if col.name == "id" && asString(values[i]) == jobID {
				return true
			}
		}
	}
	hasScope, scopeJob := false, false
	for i, col := range cols {
		value := asString(values[i])
		switch col.name {
		case "job_id", "source_job_id", "triggered_job_id", "member_job_id":
			if value == jobID {
				return true
			}
		case "scope":
			hasScope = true
			scopeJob = value == "job"
		case "scope_id":
			if hasScope && scopeJob && value == jobID {
				return true
			}
		}
	}
	return false
}

func asString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func redactString(text string, literals []string, patterns []*regexp.Regexp) (string, int) {
	count := 0
	for _, literal := range literals {
		count += strings.Count(text, literal)
		text = strings.ReplaceAll(text, literal, redactReplacement)
	}
	for _, pattern := range patterns {
		matches := pattern.FindAllStringIndex(text, -1)
		count += len(matches)
		text = pattern.ReplaceAllString(text, redactReplacement)
	}
	return text, count
}

func redactResultDir(root string, literals []string, patterns []*regexp.Regexp, report *RedactReport) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytesBinary(data) {
			rel, _ := filepath.Rel(root, path)
			report.SkippedFiles = append(report.SkippedFiles, filepath.ToSlash(rel))
			return nil
		}
		text := string(data)
		replaced, count := redactString(text, literals, patterns)
		if count == 0 {
			return nil
		}
		tmp := path + ".redact-tmp"
		if err := os.WriteFile(tmp, []byte(replaced), 0o600); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		report.FileMatches += count
		return nil
	})
}

func bytesBinary(data []byte) bool { return bytesIndexByte(data, 0) >= 0 || !utf8.Valid(data) }

func bytesIndexByte(data []byte, target byte) int {
	for i, b := range data {
		if b == target {
			return i
		}
	}
	return -1
}
