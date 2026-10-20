package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Settings are the extension settings (docs/design/2026-10-20-work-noise-and-db-settings-design.md):
// runtime-adjustable knobs — prompts, thresholds, exclusion lists — that live in the
// database instead of config.yaml. The table is a plain key/value store; typing,
// defaults, validation and the in-process cache belong to internal/settings, so this
// layer stays a dumb row store (it reports facts, it does not judge).
//
// settings_meta holds the table's generation (settings_version). Every write bumps it in
// the SAME transaction as the row, so a reader in another process never sees a value
// without the version that announces it.

// SettingsMetaVersion is the settings_meta key holding the settings table's generation.
// A reader that caches values compares its generation with this one and reloads on a
// mismatch (internal/settings does exactly that, the way httpapi caches auth grants).
const SettingsMetaVersion = "settings_version"

// Setting is one row of the settings table. Value is the stored text form of the
// setting's value (the settings package owns the encoding: decimal for ints, "true" /
// "false" for bools, a JSON array for lists, the raw text for strings).
type Setting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

const selectSettingCols = `SELECT key, value, COALESCE(updated_by,''), COALESCE(updated_at,0)
  FROM settings`

// ListSettings returns every stored setting, ordered by key. Settings that were never
// written have no row at all: the caller falls back to the registry default.
func (s *Store) ListSettings() ([]Setting, error) {
	rows, err := s.db.Query(selectSettingCols + ` ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("jobstore: list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Setting
	for rows.Next() {
		var st Setting
		if err := rows.Scan(&st.Key, &st.Value, &st.UpdatedBy, &st.UpdatedAt); err != nil {
			return nil, fmt.Errorf("jobstore: scan setting: %w", err)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobstore: list settings: %w", err)
	}
	return out, nil
}

// GetSetting returns one stored setting; ok is false when the key was never written (not
// an error — the default then applies).
func (s *Store) GetSetting(key string) (Setting, bool, error) {
	var st Setting
	err := s.db.QueryRow(selectSettingCols+` WHERE key = ?`, key).
		Scan(&st.Key, &st.Value, &st.UpdatedBy, &st.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Setting{}, false, nil
	}
	if err != nil {
		return Setting{}, false, fmt.Errorf("jobstore: get setting %q: %w", key, err)
	}
	return st, true, nil
}

// PutSetting writes (or overwrites) one setting and bumps the generation in the same
// transaction. It returns the new generation, which the caller can use to refresh its
// cache without a second read.
func (s *Store) PutSetting(key, value, by string) (int64, error) {
	if strings.TrimSpace(key) == "" {
		return 0, errors.New("jobstore: put setting: empty key")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("jobstore: put setting: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO settings (key, value, updated_by, updated_at)
  VALUES (?,?,?,?)
  ON CONFLICT(key) DO UPDATE SET value = excluded.value,
    updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		key, value, by, s.unixNow()); err != nil {
		return 0, fmt.Errorf("jobstore: put setting %q: %w", key, err)
	}
	ver, err := bumpSettingsVersion(tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("jobstore: put setting %q: commit: %w", key, err)
	}
	return ver, nil
}

// DeleteSetting removes one stored setting (the registry default applies again) and bumps
// the generation when a row was actually deleted. existing reports whether the key was
// stored.
func (s *Store) DeleteSetting(key string) (existing bool, version int64, err error) {
	if strings.TrimSpace(key) == "" {
		return false, 0, errors.New("jobstore: delete setting: empty key")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, fmt.Errorf("jobstore: delete setting: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, key)
	if err != nil {
		return false, 0, fmt.Errorf("jobstore: delete setting %q: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, 0, fmt.Errorf("jobstore: delete setting %q: %w", key, err)
	}
	if n == 0 {
		ver, err := s.SettingsVersion()
		return false, ver, err
	}
	ver, err := bumpSettingsVersion(tx)
	if err != nil {
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("jobstore: delete setting %q: commit: %w", key, err)
	}
	return true, ver, nil
}

// SettingsVersion returns settings_meta.settings_version: the generation of the settings
// table, read by caches to decide whether their snapshot is still current. A database
// without the row (never written) reads 0.
func (s *Store) SettingsVersion() (int64, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings_meta WHERE key = ?`, SettingsMetaVersion).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("jobstore: read settings_meta.%s: %w", SettingsMetaVersion, err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jobstore: settings_meta.%s = %q is not a number", SettingsMetaVersion, v)
	}
	return n, nil
}

// bumpSettingsVersion increments settings_meta.settings_version inside tx and returns the
// new generation. Every settings write calls it in its own transaction, so a row and the
// generation that announces it always land together.
func bumpSettingsVersion(tx *sql.Tx) (int64, error) {
	if _, err := tx.Exec(`INSERT INTO settings_meta(key, value) VALUES (?, '1')
  ON CONFLICT(key) DO UPDATE SET value = CAST(CAST(settings_meta.value AS INTEGER) + 1 AS TEXT)`,
		SettingsMetaVersion); err != nil {
		return 0, fmt.Errorf("jobstore: bump settings_meta.%s: %w", SettingsMetaVersion, err)
	}
	var v string
	if err := tx.QueryRow(`SELECT value FROM settings_meta WHERE key = ?`, SettingsMetaVersion).Scan(&v); err != nil {
		return 0, fmt.Errorf("jobstore: read settings_meta.%s: %w", SettingsMetaVersion, err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("jobstore: settings_meta.%s = %q is not a number", SettingsMetaVersion, v)
	}
	return n, nil
}
