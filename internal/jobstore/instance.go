package jobstore

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// metaInstanceID is the settings_meta key holding this database's instance id.
const metaInstanceID = "instance_id"

// InstanceID returns a random id that names THIS database for as long as the file lives:
// created on first use, then stable across restarts. A hub and a worker keep separate
// databases, so the id tells whose job rows a record of "who created this" refers to (a
// temporary checkout names its creator, and a sweep only reclaims its own — gofer-0eks).
// It lives in settings_meta without touching the settings generation, so it is invisible
// to the settings surface; an older binary ignores the row.
func (s *Store) InstanceID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("jobstore: instance id: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`INSERT INTO settings_meta(key, value) VALUES (?, ?)
  ON CONFLICT(key) DO NOTHING`, metaInstanceID, hex.EncodeToString(b[:])); err != nil {
		return "", fmt.Errorf("jobstore: instance id: %w", err)
	}
	var id string
	if err := s.db.QueryRow(`SELECT value FROM settings_meta WHERE key = ?`, metaInstanceID).Scan(&id); err != nil {
		return "", fmt.Errorf("jobstore: instance id: %w", err)
	}
	return id, nil
}
