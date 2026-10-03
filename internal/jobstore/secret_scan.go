package jobstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SecretScanFilter narrows a scan without carrying authorization policy into
// the store. HTTP callers set OwnerID for non-admin requests.
type SecretScanFilter struct {
	ProjectKey string
	SinceUnix  int64
	OwnerID    string
}

// SecretScanLocation identifies a matching text position without carrying the
// matched value back to the caller.
type SecretScanLocation struct {
	Position string `json:"position"`
	Matches  int    `json:"matches"`
}

type SecretScanJob struct {
	JobID     string               `json:"job_id"`
	Project   string               `json:"project,omitempty"`
	Status    string               `json:"status"`
	Title     string               `json:"title,omitempty"`
	Locations []SecretScanLocation `json:"locations"`
}

// SecretScanReport is aggregate-only: it intentionally contains no literal,
// regex body, matched text, or file contents.
type SecretScanReport struct {
	Jobs    []SecretScanJob `json:"jobs"`
	Running []SecretScanJob `json:"running,omitempty"`
}

type SecretBatchRedact struct {
	JobID  string       `json:"job_id"`
	Report RedactReport `json:"report"`
}

type SecretBatchRedactReport struct {
	Scanned   SecretScanReport    `json:"scanned"`
	Redacted  []SecretBatchRedact `json:"redacted"`
	WALPurged bool                `json:"wal_purged"`
	Vacuumed  bool                `json:"vacuumed,omitempty"`
}

func (r SecretScanReport) String() string {
	return fmt.Sprintf("jobs=%d running=%d", len(r.Jobs), len(r.Running))
}

// RedactSecrets scans once, reuses RedactJob's column/file implementation for
// every terminal hit, and checkpoints the WAL once after the batch.
func (s *Store) RedactSecrets(literals, patterns []string, filter SecretScanFilter, vacuum bool) (SecretBatchRedactReport, error) {
	scanned, err := s.ScanSecrets(literals, patterns, filter)
	if err != nil {
		return SecretBatchRedactReport{}, err
	}
	out := SecretBatchRedactReport{Scanned: scanned, Redacted: make([]SecretBatchRedact, 0, len(scanned.Jobs))}
	for _, job := range scanned.Jobs {
		report, err := s.redactJob(job.JobID, literals, patterns, false)
		if err != nil {
			return SecretBatchRedactReport{}, err
		}
		out.Redacted = append(out.Redacted, SecretBatchRedact{JobID: job.JobID, Report: report})
	}
	s.purgeWAL()
	out.WALPurged = true
	if vacuum {
		s.writeMu.Lock()
		_, err := s.db.Exec(`VACUUM`)
		s.writeMu.Unlock()
		if err != nil {
			return SecretBatchRedactReport{}, fmt.Errorf("jobstore: secret scan vacuum: %w", err)
		}
		out.Vacuumed = true
	}
	return out, nil
}

// ScanSecrets searches the same SQLite text-column and result-directory
// surfaces that RedactJob handles, plus comments/events discovered through the
// same job ownership rule. It never returns source text.
func (s *Store) ScanSecrets(literals, patterns []string, filter SecretScanFilter) (SecretScanReport, error) {
	compiled, err := compileSecretPatterns(literals, patterns)
	if err != nil {
		return SecretScanReport{}, err
	}
	args := make([]any, 0, 3)
	where := []string{"1=1"}
	if filter.ProjectKey != "" {
		where = append(where, "project_key = ?")
		args = append(args, filter.ProjectKey)
	}
	if filter.SinceUnix > 0 {
		where = append(where, "started_at >= ?")
		args = append(args, filter.SinceUnix)
	}
	if filter.OwnerID != "" {
		where = append(where, "COALESCE(caller_id,'') = ?")
		args = append(args, filter.OwnerID)
	}
	q := `SELECT id, project_key, status, COALESCE(request_json,''), COALESCE(result_dir,'')
          FROM jobs WHERE ` + strings.Join(where, " AND ") + ` ORDER BY started_at ASC, id ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return SecretScanReport{}, fmt.Errorf("jobstore: secret scan jobs: %w", err)
	}
	defer rows.Close()
	var report SecretScanReport
	for rows.Next() {
		var id, project, status, requestJSON, resultDir string
		if err := rows.Scan(&id, &project, &status, &requestJSON, &resultDir); err != nil {
			return SecretScanReport{}, fmt.Errorf("jobstore: secret scan row: %w", err)
		}
		hit, err := s.scanJobSecrets(id, resultDir, literals, compiled)
		if err != nil {
			return SecretScanReport{}, err
		}
		if !hit {
			continue
		}
		job := SecretScanJob{JobID: id, Project: project, Status: status, Locations: s.scanLocationsForJob(id, resultDir, literals, compiled)}
		var title struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(requestJSON), &title) == nil {
			job.Title, _ = redactString(title.Title, literals, compiled)
		}
		if isTerminalJobStatus(status) {
			report.Jobs = append(report.Jobs, job)
		} else {
			report.Running = append(report.Running, job)
		}
	}
	if err := rows.Err(); err != nil {
		return SecretScanReport{}, fmt.Errorf("jobstore: secret scan rows: %w", err)
	}
	return report, nil
}

func compileSecretPatterns(literals, patterns []string) ([]*regexp.Regexp, error) {
	if len(literals) == 0 && len(patterns) == 0 {
		return nil, errors.New("jobstore: secret scan: at least one literal or pattern is required")
	}
	for _, literal := range literals {
		if literal == "" {
			return nil, errors.New("jobstore: secret scan: empty literal")
		}
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern == "" {
			return nil, errors.New("jobstore: secret scan: empty pattern")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("jobstore: secret scan: invalid pattern: %w", err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

func (s *Store) scanJobSecrets(jobID, resultDir string, literals []string, patterns []*regexp.Regexp) (bool, error) {
	hit := false
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return false, fmt.Errorf("jobstore: secret scan tables: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return false, fmt.Errorf("jobstore: secret scan table: %w", err)
		}
		matched, err := s.scanJobTable(table, jobID, literals, patterns)
		if err != nil {
			return false, err
		}
		hit = hit || matched
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if resultDir != "" {
		matched, err := scanResultDir(resultDir, literals, patterns)
		if err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("jobstore: secret scan result dir: %w", err)
		}
		hit = hit || matched
	}
	return hit, nil
}

func (s *Store) scanJobTable(table, jobID string, literals []string, patterns []*regexp.Regexp) (bool, error) {
	ident := quoteIdent(table)
	info, err := s.db.Query(`PRAGMA table_info(` + ident + `)`)
	if err != nil {
		return false, fmt.Errorf("jobstore: secret scan table info: %w", err)
	}
	var cols []tableColumn
	for info.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := info.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			info.Close()
			return false, err
		}
		cols = append(cols, tableColumn{name: name, text: strings.EqualFold(typ, "TEXT") || typ == ""})
	}
	info.Close()
	if len(cols) == 0 {
		return false, nil
	}
	data, err := s.db.Query(`SELECT rowid, * FROM ` + ident)
	if err != nil {
		return false, nil
	}
	defer data.Close()
	hit := false
	for data.Next() {
		values := make([]any, len(cols)+1)
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := data.Scan(ptrs...); err != nil {
			return false, err
		}
		if !jobRowBelongs(table, cols, values[1:], jobID) {
			continue
		}
		for i, col := range cols {
			if !col.text {
				continue
			}
			text, ok := values[i+1].(string)
			if ok && secretMatchCount(text, literals, patterns) > 0 {
				hit = true
			}
		}
	}
	return hit, data.Err()
}

func (s *Store) scanLocationsForJob(jobID, resultDir string, literals []string, patterns []*regexp.Regexp) []SecretScanLocation {
	// Locations are deliberately recomputed from the same surfaces; only names
	// and counts are exposed. A failure here is represented by an empty list while
	// the existence query above remains fail-closed.
	locations := make([]SecretScanLocation, 0)
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var table string
			if rows.Scan(&table) != nil {
				continue
			}
			locations = append(locations, s.scanTableLocations(table, jobID, literals, patterns)...)
		}
	}
	if resultDir != "" {
		_ = filepath.WalkDir(resultDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || strings.HasPrefix(strings.ToUpper(entry.Name()), "CLAUDE") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr == nil && !bytesBinary(data) {
				if count := secretMatchCount(string(data), literals, patterns); count > 0 {
					rel, _ := filepath.Rel(resultDir, path)
					locations = append(locations, SecretScanLocation{Position: "file:" + filepath.ToSlash(rel), Matches: count})
				}
			}
			return nil
		})
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].Position < locations[j].Position })
	return locations
}

func (s *Store) scanTableLocations(table, jobID string, literals []string, patterns []*regexp.Regexp) []SecretScanLocation {
	ident := quoteIdent(table)
	info, err := s.db.Query(`PRAGMA table_info(` + ident + `)`)
	if err != nil {
		return nil
	}
	var cols []tableColumn
	for info.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if info.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk) == nil {
			cols = append(cols, tableColumn{name: name, text: strings.EqualFold(typ, "TEXT") || typ == ""})
		}
	}
	info.Close()
	rows, err := s.db.Query(`SELECT rowid, * FROM ` + ident)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SecretScanLocation
	for rows.Next() {
		values := make([]any, len(cols)+1)
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if rows.Scan(ptrs...) != nil || !jobRowBelongs(table, cols, values[1:], jobID) {
			continue
		}
		for i, col := range cols {
			if col.text {
				if text, ok := values[i+1].(string); ok {
					if count := secretMatchCount(text, literals, patterns); count > 0 {
						out = append(out, SecretScanLocation{Position: "db:" + table + "." + col.name, Matches: count})
					}
				}
			}
		}
	}
	return out
}

func secretMatchCount(text string, literals []string, patterns []*regexp.Regexp) int {
	_, count := redactString(text, literals, patterns)
	return count
}

func scanResultDir(root string, literals []string, patterns []*regexp.Regexp) (bool, error) {
	hit := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasPrefix(strings.ToUpper(entry.Name()), "CLAUDE") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytesBinary(data) && secretMatchCount(string(data), literals, patterns) > 0 {
			hit = true
		}
		return nil
	})
	return hit, err
}
