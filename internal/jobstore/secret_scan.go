package jobstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
		locations, err := s.scanLocationsForJob(id, resultDir, literals, compiled)
		if err != nil {
			return SecretScanReport{}, err
		}
		if len(locations) == 0 {
			continue
		}
		job := SecretScanJob{JobID: id, Project: project, Status: status, Locations: locations}
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

func (s *Store) scanLocationsForJob(jobID, resultDir string, literals []string, patterns []*regexp.Regexp) ([]SecretScanLocation, error) {
	locations := make([]SecretScanLocation, 0)
	err := forEachJobDBText(s.db, jobID, func(table, column string, _ int64, value string) error {
		if count := secretMatchCount(value, literals, patterns); count > 0 {
			locations = append(locations, SecretScanLocation{
				Position: safeScanLabel("db:"+table+"."+column, literals, patterns), Matches: count,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("jobstore: secret scan database: %w", err)
	}
	if resultDir != "" {
		err = forEachResultText(resultDir, func(_ string, rel, value string) error {
			if count := secretMatchCount(value, literals, patterns); count > 0 {
				locations = append(locations, SecretScanLocation{
					Position: safeScanLabel("file:"+rel, literals, patterns), Matches: count,
				})
			}
			return nil
		}, nil)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("jobstore: secret scan result dir: %w", err)
		}
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].Position < locations[j].Position })
	return locations, nil
}

func safeScanLabel(value string, literals []string, patterns []*regexp.Regexp) string {
	masked, _ := redactString(value, literals, patterns)
	return masked
}
func secretMatchCount(text string, literals []string, patterns []*regexp.Regexp) int {
	_, count := redactString(text, literals, patterns)
	return count
}
