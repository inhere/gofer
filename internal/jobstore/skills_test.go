package jobstore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/skill"

	_ "modernc.org/sqlite"
)

// sampleSkill builds a skill index entry for the CRUD tests.
func sampleSkill(name string) skill.Skill {
	return skill.Skill{
		Name:        name,
		Description: "how to " + name,
		Source:      "zip",
		SourceRef:   "/tmp/" + name + ".zip",
		Version:     "v-" + name,
		Files: []skill.File{
			{Path: "SKILL.md", SHA256: "aa", Size: 10},
			{Path: "ref/a.md", SHA256: "bb", Size: 20},
		},
		Size:      30,
		UpdatedAt: 1700000000,
		UpdatedBy: "tester",
	}
}

func TestSkillsTableExists(t *testing.T) {
	s := openTest(t)
	for _, col := range []string{
		"name", "description", "source", "source_ref", "version",
		"files_json", "size", "updated_at", "updated_by",
	} {
		assert.True(t, tableHasColumn(t, s, "skills", col))
	}
}

// TestSkillCRUDRoundTrip drives the index the skill library is listed from: every
// column round-trips, files travel as JSON (the []skill.File tags), the listing is
// name-ordered, and the two "drifted index" cases (update/delete of a missing name)
// are errors rather than silent no-ops.
func TestSkillCRUDRoundTrip(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertSkill(sampleSkill("zeta")))
	assert.NoErr(t, s.InsertSkill(sampleSkill("alpha")))

	got, ok, err := s.GetSkill("zeta")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "how to zeta", got.Description)
	assert.Eq(t, "zip", got.Source)
	assert.Eq(t, "/tmp/zeta.zip", got.SourceRef)
	assert.Eq(t, "v-zeta", got.Version)
	assert.Eq(t, int64(30), got.Size)
	assert.Eq(t, int64(1700000000), got.UpdatedAt)
	assert.Eq(t, "tester", got.UpdatedBy)
	assert.Len(t, got.Files, 2)
	assert.Eq(t, "SKILL.md", got.Files[0].Path)
	assert.Eq(t, "aa", got.Files[0].SHA256)
	assert.Eq(t, int64(10), got.Files[0].Size)
	assert.Eq(t, "ref/a.md", got.Files[1].Path)
	assert.Eq(t, "bb", got.Files[1].SHA256)
	assert.Eq(t, int64(20), got.Files[1].Size)

	all, err := s.ListSkills()
	assert.NoErr(t, err)
	assert.Len(t, all, 2)
	assert.Eq(t, "alpha", all[0].Name)
	assert.Eq(t, "zeta", all[1].Name)

	upd := sampleSkill("zeta")
	upd.Description = "changed"
	upd.Version = "v2"
	upd.Files = upd.Files[:1]
	upd.UpdatedAt = 1700000100
	upd.UpdatedBy = "someone-else"
	assert.NoErr(t, s.UpdateSkill(upd))
	got, ok, err = s.GetSkill("zeta")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "changed", got.Description)
	assert.Eq(t, "v2", got.Version)
	assert.Eq(t, int64(1700000100), got.UpdatedAt)
	assert.Eq(t, "someone-else", got.UpdatedBy)
	assert.Len(t, got.Files, 1)

	// A skill with no file list (never true for a real skill, but the column must not
	// invent one) reads back as no files.
	assert.NoErr(t, s.InsertSkill(skill.Skill{Name: "empty"}))
	empty, ok, err := s.GetSkill("empty")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Len(t, empty.Files, 0)
	assert.Eq(t, int64(0), empty.Size)

	assert.Err(t, s.InsertSkill(sampleSkill("zeta")))  // duplicate name is not an upsert
	assert.Err(t, s.UpdateSkill(sampleSkill("ghost"))) // drifted index
	assert.Err(t, s.DeleteSkill("ghost"))
	assert.NoErr(t, s.DeleteSkill("alpha"))
	_, ok, err = s.GetSkill("alpha")
	assert.NoErr(t, err)
	assert.False(t, ok)
	all, err = s.ListSkills()
	assert.NoErr(t, err)
	assert.Len(t, all, 2) // zeta + empty
}

// TestMigrateAddsSkillsToOldDB simulates a database written before JOB-10: the jobs
// table exists without skills_json and the skills table does not exist at all.
// Re-Open must add the column, create the table, read the old row as "no skills"
// (never a fabricated list) and round-trip a new value through both.
func TestMigrateAddsSkillsToOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	assert.NoErr(t, err)
	_, err = raw.Exec(`CREATE TABLE jobs (
	  id           TEXT PRIMARY KEY,
	  project_key  TEXT NOT NULL,
	  agent        TEXT NOT NULL,
	  runner       TEXT NOT NULL,
	  worker_id    TEXT,
	  status       TEXT NOT NULL,
	  exit_code    INTEGER NOT NULL DEFAULT 0,
	  cwd          TEXT,
	  result_dir   TEXT NOT NULL,
	  request_json TEXT,
	  error        TEXT,
	  started_at   INTEGER NOT NULL,
	  ended_at     INTEGER,
	  updated_at   INTEGER NOT NULL
	)`)
	assert.NoErr(t, err)
	_, err = raw.Exec(`INSERT INTO jobs
	  (id, project_key, agent, runner, status, result_dir, started_at, updated_at)
	  VALUES ('old-1','proj','claude','local','done','/r',1,2)`)
	assert.NoErr(t, err)
	assert.NoErr(t, raw.Close())

	s, err := Open(path)
	assert.NoErr(t, err)
	defer s.Close()

	assert.True(t, tableHasColumn(t, s, "jobs", "skills_json"))
	for _, col := range []string{"name", "description", "source", "source_ref", "version", "files_json"} {
		assert.True(t, tableHasColumn(t, s, "skills", col))
	}

	rec, ok, err := s.GetJob("old-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "", rec.SkillsJSON)

	rec.SkillsJSON = `["house-rules"]`
	assert.NoErr(t, s.UpsertJob(rec))
	got, ok, err := s.GetJob("old-1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, `["house-rules"]`, got.SkillsJSON)

	assert.NoErr(t, s.InsertSkill(sampleSkill("house-rules")))
	_, ok, err = s.GetSkill("house-rules")
	assert.NoErr(t, err)
	assert.True(t, ok)
}
