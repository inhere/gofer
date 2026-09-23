package jobstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/skill"
)

// skills.go is the SQLite index of the skill library (JOB-10, design §一.1): one row
// per skill directory under <config-dir>/skills/<name>/. The files themselves never
// come near the database — they are the operator's config-adjacent assets — and the
// row keeps only what `skill ls`, the binding resolver and the change detection
// need: where the skill came from, a version hash and the per-file sha256 list.
//
// This is where the skill package's Repo seam is implemented, and the direction is
// deliberate: jobstore imports skill (data layer implements the interface the
// service declares), never the other way round — skill depends on nothing but the
// standard library and util, so it stays unit-testable without a database.
//
// Files travel as files_json with the json tags of skill.File, so the column and the
// in-memory index cannot drift on field names.
var _ skill.Repo = (*Store)(nil)

const selectSkillCols = `SELECT name, COALESCE(description,''), COALESCE(source,''), COALESCE(source_ref,''),
  COALESCE(version,''), COALESCE(files_json,''), COALESCE(size,0), COALESCE(updated_at,0), COALESCE(updated_by,'')
  FROM skills`

// scanSkill reads one row (in selectSkillCols order) into a skill.Skill.
func scanSkill(sc rowScanner) (skill.Skill, error) {
	var (
		s         skill.Skill
		filesJSON string
	)
	err := sc.Scan(
		&s.Name, &s.Description, &s.Source, &s.SourceRef,
		&s.Version, &filesJSON, &s.Size, &s.UpdatedAt, &s.UpdatedBy,
	)
	if err != nil {
		return skill.Skill{}, err
	}
	if files, derr := decodeSkillFiles(filesJSON); derr != nil {
		return skill.Skill{}, fmt.Errorf("skill %q: %w", s.Name, derr)
	} else {
		s.Files = files
	}
	return s, nil
}

// encodeSkillFiles marshals the file index; no files (never true for a real skill —
// SKILL.md is required) is stored as NULL so "no index" stays distinguishable from
// an empty one.
func encodeSkillFiles(files []skill.File) (any, error) {
	if len(files) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// decodeSkillFiles reads the column back. An empty column reads as no files, and a
// corrupt one is an ERROR rather than an empty list: silently reporting "this skill
// has no files" would make `skill ls` and the mount path disagree with the disk in a
// way nobody can see.
func decodeSkillFiles(raw string) ([]skill.File, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var files []skill.File
	if err := json.Unmarshal([]byte(raw), &files); err != nil {
		return nil, fmt.Errorf("files_json: %w", err)
	}
	return files, nil
}

// InsertSkill persists a NEW skill row. A duplicate name is an error, not an upsert:
// the skill store replaces an existing skill through UpdateSkill once its tree has
// been swapped in, so a silent insert-over would hide a caller bug.
func (s *Store) InsertSkill(k skill.Skill) error {
	if k.Name == "" {
		return errors.New("jobstore: InsertSkill: empty skill name")
	}
	filesJSON, err := encodeSkillFiles(k.Files)
	if err != nil {
		return fmt.Errorf("jobstore: insert skill %q: files_json: %w", k.Name, err)
	}
	const q = `INSERT INTO skills
  (name, description, source, source_ref, version, files_json, size, updated_at, updated_by)
  VALUES (?,?,?,?,?,?,?,?,?)`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(q,
		k.Name, k.Description, k.Source, k.SourceRef, k.Version, filesJSON, k.Size, k.UpdatedAt, k.UpdatedBy,
	); err != nil {
		return fmt.Errorf("jobstore: insert skill %q: %w", k.Name, err)
	}
	return nil
}

// UpdateSkill replaces an existing skill row (name is the key and never changes: a
// rename upstream is a re-import, not an update). A name that is not in the table is
// an error rather than a no-op — the caller has just put the skill's files in place,
// so "0 rows updated" means the index and the library have drifted apart and the
// caller must hear about it.
func (s *Store) UpdateSkill(k skill.Skill) error {
	if k.Name == "" {
		return errors.New("jobstore: UpdateSkill: empty skill name")
	}
	filesJSON, err := encodeSkillFiles(k.Files)
	if err != nil {
		return fmt.Errorf("jobstore: update skill %q: files_json: %w", k.Name, err)
	}
	const q = `UPDATE skills SET description=?, source=?, source_ref=?, version=?,
  files_json=?, size=?, updated_at=?, updated_by=? WHERE name=?`
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(q,
		k.Description, k.Source, k.SourceRef, k.Version, filesJSON, k.Size, k.UpdatedAt, k.UpdatedBy, k.Name,
	)
	if err != nil {
		return fmt.Errorf("jobstore: update skill %q: %w", k.Name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("jobstore: update skill %q: %w", k.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("jobstore: update skill %q: no such skill", k.Name)
	}
	return nil
}

// GetSkill returns one skill by name. The bool is false (with a nil error) when the
// library has no such skill, distinguishing "not imported" from a real query error.
func (s *Store) GetSkill(name string) (skill.Skill, bool, error) {
	k, err := scanSkill(s.db.QueryRow(selectSkillCols+" WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Skill{}, false, nil
	}
	if err != nil {
		return skill.Skill{}, false, fmt.Errorf("jobstore: get skill %q: %w", name, err)
	}
	return k, true, nil
}

// ListSkills returns every skill, name-ordered: the library listing and the
// `--skill` completion both want a stable order, and name is the key.
func (s *Store) ListSkills() ([]skill.Skill, error) {
	rows, err := s.db.Query(selectSkillCols + " ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("jobstore: list skills: %w", err)
	}
	defer rows.Close()
	var out []skill.Skill
	for rows.Next() {
		k, serr := scanSkill(rows)
		if serr != nil {
			return nil, fmt.Errorf("jobstore: list skills: %w", serr)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list skills: %w", err)
	}
	return out, nil
}

// DeleteSkill removes one skill row. A missing name is an error for the same reason
// UpdateSkill's is: the caller has already decided to delete this skill and drops
// its directory next, so a row that is not there means the index drifted.
func (s *Store) DeleteSkill(name string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM skills WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("jobstore: delete skill %q: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("jobstore: delete skill %q: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("jobstore: delete skill %q: no such skill", name)
	}
	return nil
}
