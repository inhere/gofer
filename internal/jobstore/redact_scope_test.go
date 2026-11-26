package jobstore

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// legacyJobDBText is the enumeration the scan and the redaction used before the
// predicate moved into SQL: every row of every table is read and jobRowBelongs
// decides in Go. It is the reference the narrowed read must match row for row, so
// the equivalence tests keep pinning the set of rows a scan reaches and a redaction
// rewrites, not just their count.
func legacyJobDBText(t *testing.T, q *sql.DB, jobID string, visit func(table, column string, rowID int64, value string) error) {
	t.Helper()
	rows, err := q.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		info, err := q.Query(`PRAGMA table_info(` + quoteIdent(table) + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var cols []tableColumn
		for info.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue any
			if err := info.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, tableColumn{name: name, text: strings.EqualFold(typ, "TEXT") || typ == ""})
		}
		info.Close()
		if len(cols) == 0 {
			continue
		}
		data, err := q.Query(`SELECT rowid, * FROM ` + quoteIdent(table))
		if err != nil {
			continue // WITHOUT ROWID/system tables.
		}
		for data.Next() {
			values := make([]any, len(cols)+1)
			ptrs := make([]any, len(values))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := data.Scan(ptrs...); err != nil {
				data.Close()
				t.Fatal(err)
			}
			if !jobRowBelongs(table, cols, values[1:], jobID) {
				continue
			}
			rowID, ok := values[0].(int64)
			if !ok {
				continue
			}
			for i, col := range cols {
				if !col.text {
					continue
				}
				if value, ok := values[i+1].(string); ok && value != "" {
					if err := visit(table, col.name, rowID, value); err != nil {
						data.Close()
						t.Fatal(err)
					}
				}
			}
		}
		data.Close()
	}
}

// legacyJobDBRows is legacyJobDBText as "{table}.{column}[{rowid}]={value}" lines.
func legacyJobDBRows(t *testing.T, q *sql.DB, jobID string) []string {
	t.Helper()
	var out []string
	legacyJobDBText(t, q, jobID, func(table, column string, rowID int64, value string) error {
		out = append(out, fmt.Sprintf("%s.%s[%d]=%s", table, column, rowID, value))
		return nil
	})
	return out
}

// narrowedJobDBRows is what the production enumeration reaches for the job.
func narrowedJobDBRows(t *testing.T, q *sql.DB, jobID string) []string {
	t.Helper()
	var out []string
	if err := forEachJobDBText(q, jobID, func(table, column string, rowID int64, value string) error {
		out = append(out, fmt.Sprintf("%s.%s[%d]=%s", table, column, rowID, value))
		return nil
	}); err != nil {
		t.Fatalf("enumerate job %q: %v", jobID, err)
	}
	return out
}

func assertSameRows(t *testing.T, jobID string, want, got []string) {
	t.Helper()
	if strings.Join(want, "\n") == strings.Join(got, "\n") {
		return
	}
	t.Fatalf("job %q: narrowed enumeration differs from the table-wide reference\n want (%d rows): %v\n  got (%d rows): %v",
		jobID, len(want), want, len(got), got)
}

const jobScopeSecret = "job-scope-secret-1234567890"

// seedJobScopeRows writes, for one job id, a row in a table of every ownership shape
// (job_id, source_job_id, member_job_id, the comments scope pair, the jobs primary
// key), plus one "decoy" row per shape whose job id sits in a column that does NOT
// name the row's owner (leader_job_id, new_job_id, filter_job_id, last_job_id,
// handed_off_job_id, capture_source_job) and a decoy comment under another scope.
// Every seeded value carries jobScopeSecret, so a predicate that reaches a decoy or
// misses a real row shows up as a difference in the enumerated rows.
func seedJobScopeRows(t *testing.T, s *Store, jobID string, startedAt int64) {
	t.Helper()
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	job := sampleJob(jobID, "proj", startedAt)
	job.Status, job.EndedAt, job.UpdatedAt = "done", startedAt+1, startedAt+1
	job.Error = "job error " + jobScopeSecret
	if err := s.UpsertJob(job); err != nil {
		t.Fatal(err)
	}
	// A job resumed from this one carries its id in jobs.source_job_id: the jobs row of
	// the derived job is this job's row too (jobRowBelongs matches source_job_id).
	derived := sampleJob(jobID+"-derived", "proj", startedAt+2)
	derived.Status, derived.EndedAt, derived.UpdatedAt = "done", startedAt+3, startedAt+3
	derived.SourceJobID = jobID
	derived.Error = "derived error " + jobScopeSecret
	if err := s.UpsertJob(derived); err != nil {
		t.Fatal(err)
	}
	seed(`INSERT INTO job_events (job_id, type, detail_json, at) VALUES (?,?,?,?)`,
		jobID, "job.log", `{"line":"`+jobScopeSecret+`"}`, startedAt)
	seed(`INSERT INTO comments (id, scope, scope_id, author, author_kind, body, created_at) VALUES (?,?,?,?,?,?,?)`,
		"cm-"+jobID, CommentScopeJob, jobID, "alice", CommentAuthorUser, "comment "+jobScopeSecret, startedAt)
	seed(`INSERT INTO work_summaries (work_item_id, at, state, job_id, error) VALUES (?,?,?,?,?)`,
		"w-"+jobID, startedAt, "running", jobID, "work "+jobScopeSecret)
	seed(`INSERT INTO plan_events (plan_id, todo_id, job_id, kind, created_at) VALUES (?,?,?,?,?)`,
		"plan-1", "todo-"+jobID, jobID, "dispatch "+jobScopeSecret, startedAt)
	seed(`INSERT INTO xfers (id, op, runner, project_key, path, state, created_at, job_id, error) VALUES (?,?,?,?,?,?,?,?,?)`,
		"x-"+jobID, "push", "local", "proj", ".", "failed", startedAt, jobID, "xfer "+jobScopeSecret)
	seed(`INSERT INTO admin_audit (at, job_id, error) VALUES (?,?,?)`,
		startedAt, jobID, "admin "+jobScopeSecret)
	seed(`INSERT INTO interactions (id, job_id, type, prompt, status, created_at, answer) VALUES (?,?,?,?,?,?,?)`,
		"i-"+jobID, jobID, "approval", "proceed?", "answered", startedAt, "answer "+jobScopeSecret)
	seed(`INSERT INTO memory_candidates (job_id, project_key, text, status, created_at) VALUES (?,?,?,?,?)`,
		jobID, "proj", "memory "+jobScopeSecret, "pending", startedAt)
	seed(`INSERT INTO event_deliveries (event_seq, job_id, target, status, next_retry_at, created_at, updated_at, last_error) VALUES (?,?,?,?,?,?,?,?)`,
		startedAt, jobID, "hook", "failed", startedAt, startedAt, startedAt, "delivery "+jobScopeSecret)
	seed(`INSERT INTO trigger_receipts (trigger_name, external_event_id, request_id, body_hash, received_at, status, expires_at, job_id, last_error) VALUES (?,?,?,?,?,?,?,?,?)`,
		"trig", "evt-"+jobID, "req-"+jobID, "hash", startedAt, "done", startedAt, jobID, "receipt "+jobScopeSecret)
	seed(`INSERT INTO background_agent_runs (kind, job_id, started_at, status, error) VALUES (?,?,?,?,?)`,
		"summarize", jobID, startedAt, "done", "run "+jobScopeSecret)
	seed(`INSERT INTO plan_leader_wakes (id, plan_id, member_job_id, member_status, due_at, created_at) VALUES (?,?,?,?,?,?)`,
		"lw-"+jobID, "plan-1", jobID, "wake "+jobScopeSecret, startedAt, startedAt)
	seed(`INSERT INTO job_retries (id, source_job_id, attempt, request_json, reason, next_run_at, created_at) VALUES (?,?,?,?,?,?,?)`,
		"r-"+jobID, jobID, 1, `{}`, "retry "+jobScopeSecret, startedAt, startedAt)
	seed(`INSERT INTO job_wakeups (id, job_id, kind, instruction, created_at) VALUES (?,?,?,?,?)`,
		"wk-"+jobID, jobID, "wakeup", "wake "+jobScopeSecret, startedAt)
	seed(`INSERT INTO mail (thread_id, from_kind, from_id, to_kind, to_id, job_id, body, state, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		"thr-"+jobID, MailKindCaller, "alice", MailKindJob, jobID, jobID, "letter "+jobScopeSecret, "delivered", startedAt)

	// Decoys: the id sits in a column that does not name the row's owner.
	seed(`INSERT INTO comments (id, scope, scope_id, author, author_kind, body, created_at) VALUES (?,?,?,?,?,?,?)`,
		"cm-plan-"+jobID, CommentScopePlan, jobID, "alice", CommentAuthorUser, "plan note "+jobScopeSecret, startedAt)
	seed(`INSERT INTO plan_leader_wakes (id, plan_id, member_job_id, member_status, due_at, leader_job_id, created_at) VALUES (?,?,?,?,?,?,?)`,
		"lw-leader-"+jobID, "plan-1", "other-member", "leader "+jobScopeSecret, startedAt, jobID, startedAt)
	seed(`INSERT INTO job_retries (id, source_job_id, attempt, request_json, reason, next_run_at, new_job_id, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		"r-new-"+jobID, "other-source", 1, `{}`, "new "+jobScopeSecret, startedAt, jobID, startedAt)
	seed(`INSERT INTO job_wakeups (id, job_id, kind, instruction, filter_job_id, created_at) VALUES (?,?,?,?,?,?)`,
		"wk-filter-"+jobID, "other-wakeup", "event", "filter "+jobScopeSecret, jobID, startedAt)
	seed(`INSERT INTO schedules (id, name, cron_expr, request_json, enabled, next_run_at, created_at, updated_at, last_job_id, last_result) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"s-"+jobID, "schedule", "* * * * *", `{}`, 1, startedAt, startedAt, startedAt, jobID, "sched "+jobScopeSecret)
	seed(`INSERT INTO agent_sessions (session_id, agent, project_key, runner, cwd, state, handed_off_job_id, last_message, last_seen_at, started_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"sess-"+jobID, "claude", "proj", "local", ".", "ended", jobID, "session "+jobScopeSecret, startedAt, startedAt)
	seed(`INSERT INTO work_items (id, title, status, created_at, updated_at, capture_source_job) VALUES (?,?,?,?,?,?)`,
		"wi-"+jobID, "capture "+jobScopeSecret, "open", startedAt, startedAt, jobID)
}

// jobScopeDecoys are the seeded decoy rows of one job id and the query that reads
// the text column holding the secret in them.
func jobScopeDecoys(jobID string) []struct {
	name  string
	query string
} {
	return []struct {
		name  string
		query string
	}{
		{"comment under another scope", `SELECT body FROM comments WHERE id='cm-plan-` + jobID + `'`},
		{"plan leader wake", `SELECT member_status FROM plan_leader_wakes WHERE id='lw-leader-` + jobID + `'`},
		{"retry of another job", `SELECT reason FROM job_retries WHERE id='r-new-` + jobID + `'`},
		{"wakeup filtered by job", `SELECT instruction FROM job_wakeups WHERE id='wk-filter-` + jobID + `'`},
		{"schedule last job", `SELECT last_result FROM schedules WHERE id='s-` + jobID + `'`},
		{"session handed off job", `SELECT last_message FROM agent_sessions WHERE session_id='sess-` + jobID + `'`},
		{"work item captured from job", `SELECT title FROM work_items WHERE id='wi-` + jobID + `'`},
	}
}

// TestJobScanRowsMatchTableWideReference pins the narrowed enumeration to the
// table-wide read it replaced: the same rows, in the same order, for a real job, for
// a second job, and for a job that owns nothing.
func TestJobScanRowsMatchTableWideReference(t *testing.T) {
	s := openTest(t)
	seedJobScopeRows(t, s, "scope-target", 100)
	seedJobScopeRows(t, s, "scope-other", 200)

	for _, jobID := range []string{"scope-target", "scope-other", "scope-absent"} {
		want := legacyJobDBRows(t, s.db, jobID)
		got := narrowedJobDBRows(t, s.db, jobID)
		assertSameRows(t, jobID, want, got)
		if jobID == "scope-absent" && len(got) != 0 {
			t.Fatalf("absent job reached %d rows: %v", len(got), got)
		}
		if jobID != "scope-absent" && len(got) == 0 {
			t.Fatalf("job %q reached no rows, want its seeded rows", jobID)
		}
	}
}

// TestSecretScanLocationsMatchTableWideReference: the scan's own view — which job
// matched, in which table.column — is the reference's, so a predicate that missed a
// row or reached a decoy changes the reported locations too.
func TestSecretScanLocationsMatchTableWideReference(t *testing.T) {
	s := openTest(t)
	seedJobScopeRows(t, s, "scope-target", 100)
	seedJobScopeRows(t, s, "scope-other", 200)

	report, err := s.ScanSecrets([]string{jobScopeSecret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	hit := make(map[string]bool, len(report.Jobs))
	for _, job := range report.Jobs {
		hit[job.JobID] = true
	}
	for _, jobID := range []string{"scope-target", "scope-other"} {
		if !hit[jobID] {
			t.Fatalf("scan missed seeded job %q: %+v", jobID, report.Jobs)
		}
	}
	// Every reported job's locations are the reference's — including the jobs resumed from
	// a seeded one, whose only own row is the jobs row named by their source_job_id. The
	// comparison is per row (a job and a job resumed from it report jobs.error twice).
	for _, job := range report.Jobs {
		var got []string
		for _, location := range job.Locations {
			got = append(got, location.Position)
		}
		sort.Strings(got)
		var want []string
		legacyJobDBText(t, s.db, job.JobID, func(table, column string, _ int64, value string) error {
			if secretMatchCount(value, []string{jobScopeSecret}, nil) > 0 {
				want = append(want, "db:"+table+"."+column)
			}
			return nil
		})
		sort.Strings(want)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Fatalf("job %q scan locations = %v, want %v", job.JobID, got, want)
		}
	}
}

// TestRedactJobRewritesOnlyItsOwnRows: one job's redaction rewrites that job's rows,
// counts exactly them, and leaves every decoy and the other job's rows with the
// secret intact.
func TestRedactJobRewritesOnlyItsOwnRows(t *testing.T) {
	s := openTest(t)
	seedJobScopeRows(t, s, "scope-target", 100)
	seedJobScopeRows(t, s, "scope-other", 200)

	ownMatches := 0
	legacyJobDBText(t, s.db, "scope-target", func(_ string, _ string, _ int64, value string) error {
		_, count := redactString(value, []string{jobScopeSecret}, nil)
		ownMatches += count
		return nil
	})
	if ownMatches == 0 {
		t.Fatal("reference found no secret in the target's own rows")
	}
	report, err := s.RedactJob("scope-target", []string{jobScopeSecret}, nil)
	if err != nil {
		t.Fatalf("RedactJob: %v", err)
	}
	if report.DBMatches != ownMatches {
		t.Fatalf("db matches = %d, want %d (the target's own rows only)", report.DBMatches, ownMatches)
	}
	for _, row := range narrowedJobDBRows(t, s.db, "scope-target") {
		if strings.Contains(row, jobScopeSecret) {
			t.Fatalf("target row kept the secret: %s", row)
		}
	}
	for _, decoy := range jobScopeDecoys("scope-target") {
		var value string
		if err := s.db.QueryRow(decoy.query).Scan(&value); err != nil {
			t.Fatalf("read decoy %s: %v", decoy.name, err)
		}
		if !strings.Contains(value, jobScopeSecret) {
			t.Fatalf("decoy %s was rewritten: %q", decoy.name, value)
		}
	}
	// The other job's own rows are untouched too.
	for _, row := range narrowedJobDBRows(t, s.db, "scope-other") {
		if !strings.Contains(row, jobScopeSecret) && strings.Contains(row, "secret") {
			t.Fatalf("other job's row lost the secret: %s", row)
		}
	}
}

// TestJobRowSQLWhereMatchesGoFilter: the predicate is the Go rule's SQL twin. Every
// ownership shape the schema has (job id in a column of its own, the comments scope
// pair, the jobs primary key, and a table no column of which names the job) is tried
// against the rows jobRowBelongs accepts, so a predicate that matches another column
// or forgets a shape fails here before any scan runs.
func TestJobRowSQLWhereMatchesGoFilter(t *testing.T) {
	s := openTest(t)
	seedJobScopeRows(t, s, "scope-target", 100)
	const jobID = "scope-target"
	for _, table := range []string{"jobs", "job_events", "comments", "plan_leader_wakes",
		"job_retries", "job_wakeups", "work_summaries", "mail", "audit_events", "settings"} {
		t.Run(table, func(t *testing.T) {
			cols := tableColumns(t, s, table)
			where, args, narrowed := jobRowSQLWhere(table, cols, jobID)
			// The rows jobRowBelongs accepts, by rowid.
			goRows := make(map[int64]bool)
			legacyJobDBText(t, s.db, jobID, func(gotTable string, _ string, rowID int64, _ string) error {
				if gotTable == table {
					goRows[rowID] = true
				}
				return nil
			})
			if !narrowed {
				if len(goRows) != 0 {
					t.Fatalf("no predicate over %s, but jobRowBelongs accepts rows %v", table, goRows)
				}
				return
			}
			rows, err := s.db.Query(`SELECT rowid FROM `+quoteIdent(table)+` WHERE `+where, args...)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			defer rows.Close()
			sqlRows := make(map[int64]bool)
			for rows.Next() {
				var rowID int64
				if err := rows.Scan(&rowID); err != nil {
					t.Fatal(err)
				}
				sqlRows[rowID] = true
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if len(sqlRows) == 0 && len(goRows) == 0 {
				return
			}
			for rowID := range goRows {
				if !sqlRows[rowID] {
					t.Fatalf("%s: predicate %q misses rowid %d, jobRowBelongs accepts it", table, where, rowID)
				}
			}
			for rowID := range sqlRows {
				if !goRows[rowID] {
					t.Fatalf("%s: predicate %q reaches rowid %d, jobRowBelongs rejects it", table, where, rowID)
				}
			}
		})
	}
}

// readCountingQueryer records every statement the enumeration runs and counts the
// rows each data read returns (by running its count query against the same database).
type readCountingQueryer struct {
	db      *sql.DB
	queries []string
	args    [][]any
}

func (c *readCountingQueryer) Query(query string, args ...any) (*sql.Rows, error) {
	c.queries = append(c.queries, query)
	c.args = append(c.args, args)
	return c.db.Query(query, args...)
}

// rowsRead is the sum of the rows the recorded data reads return: every job-owned
// table read with an argument is counted, table listings and schema probes are not.
func (c *readCountingQueryer) rowsRead(t *testing.T) int {
	t.Helper()
	total := 0
	for i, query := range c.queries {
		if !strings.HasPrefix(query, `SELECT rowid, * FROM `) || len(c.args[i]) == 0 {
			continue
		}
		var n int
		if err := c.db.QueryRow(`SELECT count(*) FROM (`+query+`)`, c.args[i]...).Scan(&n); err != nil {
			t.Fatalf("count rows of %q: %v", query, err)
		}
		total += n
	}
	return total
}

// TestJobScanReadsOnlyTheJobsRows: with hundreds of unrelated jobs' rows in every
// table the scan reads, one job's enumeration returns (and therefore reads) only its
// own rows, and no read touches a table that carries no job id at all.
func TestJobScanReadsOnlyTheJobsRows(t *testing.T) {
	s := openTest(t)
	seedJobScopeRows(t, s, "scope-target", 100)
	ownRows := 0
	seenRow := make(map[string]bool)
	legacyJobDBText(t, s.db, "scope-target", func(table, _ string, rowID int64, _ string) error {
		key := fmt.Sprintf("%s/%d", table, rowID)
		if !seenRow[key] {
			seenRow[key] = true
			ownRows++
		}
		return nil
	})
	for i := 0; i < 300; i++ {
		seedJobScopeRows(t, s, fmt.Sprintf("scope-noise-%03d", i), int64(1000+i))
	}
	if got := len(narrowedJobDBRows(t, s.db, "scope-target")); got == 0 {
		t.Fatal("target reached no rows")
	}

	recorder := &readCountingQueryer{db: s.db}
	if err := forEachJobDBText(recorder, "scope-target", func(string, string, int64, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := recorder.rowsRead(t); got != ownRows {
		t.Fatalf("scan read %d rows, want %d (only the job's own rows)", got, ownRows)
	}
	for i, query := range recorder.queries {
		if !strings.HasPrefix(query, `SELECT rowid, * FROM `) {
			continue
		}
		table := strings.Trim(strings.SplitN(query, `"`, 3)[1], `"`)
		if !tableCarriesJobID(t, s, table) {
			t.Fatalf("scan read %q: the table carries no job id, it must not be read at all", query)
		}
		if !strings.Contains(query, " WHERE ") {
			t.Fatalf("scan read %q without a predicate over %d args", query, len(recorder.args[i]))
		}
	}

	// The reads of the tables that index a job-naming column are index searches, not
	// table scans. (A table with no such index, or with two independent columns naming
	// its rows — comments: its scope pair or triggered_job_id — is filtered by SQLite
	// with a scan; the row count above is the guarantee there.)
	for _, table := range []string{"job_events", "interactions", "job_followups"} {
		cols := tableColumns(t, s, table)
		where, args, narrowed := jobRowSQLWhere(table, cols, "scope-target")
		if !narrowed {
			t.Fatalf("table %s: no predicate, want one", table)
		}
		planRows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT rowid, * FROM `+quoteIdent(table)+` WHERE `+where+` ORDER BY rowid`, args...)
		if err != nil {
			t.Fatalf("explain %s: %v", table, err)
		}
		var plan []string
		for planRows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := planRows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				planRows.Close()
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		planRows.Close()
		joined := strings.Join(plan, " | ")
		if !strings.Contains(joined, "USING INDEX") || strings.Contains(joined, "SCAN "+table) {
			t.Fatalf("%s plan = %s, want an index search", table, joined)
		}
	}
}

// tableCarriesJobID reports whether the table is named by a job id at all, i.e.
// whether the narrowed enumeration may read it.
func tableCarriesJobID(t *testing.T, s *Store, table string) bool {
	t.Helper()
	for _, col := range tableColumns(t, s, table) {
		for _, name := range jobRowBelongsColumns {
			if col.name == name {
				return true
			}
		}
	}
	if table == "jobs" {
		for _, col := range tableColumns(t, s, table) {
			if col.name == "id" {
				return true
			}
		}
	}
	if table == "comments" {
		return true // the scope pair names its job rows
	}
	return false
}

// tableColumns reads a table's columns the way the enumerator does.
func tableColumns(t *testing.T, s *Store, table string) []tableColumn {
	t.Helper()
	info, err := s.db.Query(`PRAGMA table_info(` + quoteIdent(table) + `)`)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer info.Close()
	var cols []tableColumn
	for info.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := info.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, tableColumn{name: name, text: strings.EqualFold(typ, "TEXT") || typ == ""})
	}
	return cols
}
