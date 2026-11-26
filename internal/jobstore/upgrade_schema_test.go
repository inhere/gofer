package jobstore

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenUpgradesPreviousReleaseSchema opens a database built from the schema a
// previous release created (testdata/schema-v0.201.0.sql) with the current code.
// Every other test starts from a fresh database, where a statement in the base
// schema that refers to a column only a migration adds still works; on an upgraded
// database it fails and the server does not start (v0.202.0: idx_workflows_project
// referred to workflows.project_key before the migration added it).
//
// When a release changes the schema, add its schema file here too. To export one: build
// that release's tree (git archive vX.Y.Z), open an empty database with its jobstore.Open,
// then write every sqlite_master.sql (tables first, then indexes) as one statement per
// line, plus the work_kv rows (one-time migration markers) as INSERTs.
func TestOpenUpgradesPreviousReleaseSchema(t *testing.T) {
	for _, fixture := range []string{"schema-v0.201.0.sql", "schema-v0.203.0.sql", "schema-v0.204.0.sql", "schema-v0.205.1.sql", "schema-v0.208.0.sql", "schema-v0.211.0.sql", "schema-v0.212.0.sql", "schema-v0.213.0.sql", "schema-v0.214.1.sql", "schema-v0.215.0.sql", "schema-v0.218.0.sql"} {
		t.Run(fixture, func(t *testing.T) {
			ddl, err := os.ReadFile(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "gofer.db")
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(string(ddl)); err != nil {
				_ = raw.Close()
				t.Fatalf("build the %s database: %v", fixture, err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			s, err := Open(path)
			if err != nil {
				t.Fatalf("open a database from %s: %v", fixture, err)
			}
			// A second open runs the schema again over the migrated database.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatalf("reopen the migrated %s database: %v", fixture, err)
			}
			// The seat roster tables exist on an upgraded database and take a row.
			if _, err := s.CreateSeat(Seat{CallerID: "up", ProjectKey: "p", Name: "n"}); err != nil {
				t.Fatalf("create a seat on the migrated database: %v", err)
			}
			// The token lifetime columns of auth_callers are added to an old database, and the
			// admin audit table exists.
			if err := s.WithCallerTx(func(tx CallerTx) error {
				return tx.InsertCallerTx(StoredCaller{ID: "up-caller", TokenHash: "h", ExpiresAt: 99})
			}); err != nil {
				t.Fatalf("insert a caller with an expiry on the migrated database: %v", err)
			}
			if rows, err := s.ListStoredCallers(); err != nil || len(rows) == 0 || rows[len(rows)-1].ExpiresAt != 99 {
				t.Fatalf("list callers on the migrated database: %+v %v", rows, err)
			}
			if err := s.InsertAdminAuditBatch([]AdminAuditEntry{{Method: "POST", Route: "POST /x", Status: 200}}); err != nil {
				t.Fatalf("admin audit on the migrated database: %v", err)
			}
			if _, _, err := s.ListWorkflowsPage(WorkflowListFilter{}, 10, 0); err != nil {
				t.Fatalf("list workflows on the migrated database: %v", err)
			}
			// The migrated workflows table takes and returns the template attribution.
			if err := s.InsertWorkflow(Workflow{ID: "wf-up", Status: WorkflowRunning, CurrentStep: 1, TotalSteps: 1, SpecJSON: "{}",
				Template: "demo-tpl", TemplateSource: "global", CreatedAt: 1, UpdatedAt: 1}); err != nil {
				t.Fatalf("insert a template workflow on the migrated database: %v", err)
			}
			if got, ok, err := s.GetWorkflow("wf-up"); err != nil || !ok || got.Template != "demo-tpl" || got.TemplateSource != "global" {
				t.Fatalf("migrated workflow = %+v ok=%v err=%v, want template demo-tpl / global", got, ok, err)
			}
			_ = s.Close()
		})
	}
}
