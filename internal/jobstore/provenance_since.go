package jobstore

import (
	"fmt"
	"strconv"
)

// provenanceSinceKey records when this database first ran a build that stamps where a job
// came from (submitted_by_session / submitted_by_job). Jobs started before it carry no such
// stamp, so "a person submitted this" cannot be told from "an agent did" for them.
const provenanceSinceKey = "job_provenance_since"

// markProvenanceSince stores the first-open time once; later opens keep it.
func (s *Store) markProvenanceSince() error {
	if v, err := s.GetWorkKV(provenanceSinceKey); err != nil || v != "" {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO work_kv(k, v) VALUES (?,?) ON CONFLICT(k) DO NOTHING`,
		provenanceSinceKey, strconv.FormatInt(s.unixNow(), 10)); err != nil {
		return fmt.Errorf("jobstore: mark job provenance: %w", err)
	}
	return nil
}

// ProvenanceSince is the unix time from which job provenance is recorded; 0 when unknown.
func (s *Store) ProvenanceSince() int64 {
	v, err := s.GetWorkKV(provenanceSinceKey)
	if err != nil || v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

// UnixNow is the store's clock (the same one it stamps rows with).
func (s *Store) UnixNow() int64 { return s.unixNow() }
