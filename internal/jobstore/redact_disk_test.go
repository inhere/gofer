package jobstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestJobRedactLeavesNoBytesOnDisk: replacing a value in the rows is not enough —
// the old page images stay in the -wal file and in freed page space until they are
// overwritten. Redact and delete must leave no copy anywhere in the db files.
func TestJobRedactLeavesNoBytesOnDisk(t *testing.T) {
	for _, mode := range []string{"redact", "delete"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "gofer.db")
			s, err := Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			const secret = "fake-secret-on-disk-9f8e7d6c5b4a"
			job := sampleJob("disk-"+mode, "proj", 100)
			job.Status, job.EndedAt, job.UpdatedAt = "done", 200, 200
			job.RenderedCommand = "printf " + secret
			job.Error = "error " + secret
			if err := s.UpsertJob(job); err != nil {
				t.Fatal(err)
			}
			if _, err := s.InsertJobEvent(JobEvent{JobID: job.ID, Type: "job.terminal", Detail: `{"line":"` + secret + `"}`, At: 200}); err != nil {
				t.Fatal(err)
			}
			if mode == "redact" {
				if _, err := s.RedactJob(job.ID, []string{secret}, nil); err != nil {
					t.Fatal(err)
				}
			} else if err := s.DeleteJob(job.ID, "admin"); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"gofer.db", "gofer.db-wal"} {
				raw, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if bytes.Contains(raw, []byte(secret)) {
					t.Fatalf("secret bytes survived in %s after %s", name, mode)
				}
			}
		})
	}
}
