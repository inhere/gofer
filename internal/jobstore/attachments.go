package jobstore

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Attachment is one row of the attachments table: an uploaded image and, once it was sent with
// a message, the thread and message it belongs to.
type Attachment struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
	// ThreadID is empty until BindAttachment ran (an UNSENT attachment: readable by its
	// uploader only).
	ThreadID string `json:"thread_id,omitempty"`
	// RefKind / RefID name the message the attachment was sent with (decision id, job id,
	// session message id …); set together with ThreadID.
	RefKind   string `json:"ref_kind,omitempty"`
	RefID     string `json:"ref_id,omitempty"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256,omitempty"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	CreatedAt int64  `json:"created_at"`
	// ExpiresAt is the unix second the retention sweep may delete it at; 0 = never.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// Path is the stored file's path RELATIVE to the attachment root (<yyyymmdd>/<id>).
	Path string `json:"-"`
}

// Attachment errors.
var (
	// ErrAttachmentNotFound: no row with that id.
	ErrAttachmentNotFound = errors.New("jobstore: attachment not found")
	// ErrAttachmentBound: the attachment is already sent (bound to a thread).
	ErrAttachmentBound = errors.New("jobstore: attachment already bound to a thread")
)

const attachmentCols = `id,caller,thread_id,ref_kind,ref_id,mime,bytes,sha256,width,height,created_at,expires_at,path`

func scanAttachment(sc rowScanner) (Attachment, error) {
	var a Attachment
	err := sc.Scan(&a.ID, &a.Caller, &a.ThreadID, &a.RefKind, &a.RefID, &a.Mime, &a.Bytes, &a.SHA256,
		&a.Width, &a.Height, &a.CreatedAt, &a.ExpiresAt, &a.Path)
	return a, err
}

// InsertAttachment stores a new (unsent) attachment row. CreatedAt defaults to now.
func (s *Store) InsertAttachment(a Attachment) error {
	a.ID = strings.TrimSpace(a.ID)
	if a.ID == "" || a.Mime == "" || a.Path == "" {
		return errors.New("jobstore: attachment id, mime and path are required")
	}
	if a.CreatedAt == 0 {
		a.CreatedAt = s.unixNow()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO attachments (`+attachmentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Caller, a.ThreadID, a.RefKind, a.RefID, a.Mime, a.Bytes, a.SHA256, a.Width, a.Height,
		a.CreatedAt, a.ExpiresAt, a.Path)
	if err != nil {
		return fmt.Errorf("jobstore: insert attachment: %w", err)
	}
	return nil
}

// GetAttachment reads one row; ok is false when it does not exist.
func (s *Store) GetAttachment(id string) (Attachment, bool, error) {
	a, err := scanAttachment(s.db.QueryRow(`SELECT `+attachmentCols+` FROM attachments WHERE id=?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, fmt.Errorf("jobstore: get attachment: %w", err)
	}
	return a, true, nil
}

// BindAttachment marks an unsent attachment as sent: it fills thread_id and the message
// reference. It fails with ErrAttachmentNotFound for an unknown id and ErrAttachmentBound when
// the attachment already has a thread (an attachment is sent at most once).
func (s *Store) BindAttachment(id, threadID, refKind, refID string) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("jobstore: bind attachment: thread id is required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE attachments SET thread_id=?, ref_kind=?, ref_id=? WHERE id=? AND thread_id=''`,
		threadID, refKind, refID, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("jobstore: bind attachment: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, ok, gerr := s.GetAttachment(id); gerr != nil {
		return gerr
	} else if !ok {
		return ErrAttachmentNotFound
	}
	return ErrAttachmentBound
}

// UnbindAttachment undoes BindAttachment for an attachment still bound to threadID — used
// when the message it was bound to (before the send, so the history shows it the moment the
// agent echoes the prompt) could not be delivered. It is a no-op for a row bound elsewhere
// or already unsent.
func (s *Store) UnbindAttachment(id, threadID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.Exec(`UPDATE attachments SET thread_id='', ref_kind='', ref_id='' WHERE id=? AND thread_id=?`,
		strings.TrimSpace(id), strings.TrimSpace(threadID)); err != nil {
		return fmt.Errorf("jobstore: unbind attachment: %w", err)
	}
	return nil
}

// DeleteAttachment removes a row (any state) and reports whether one existed.
func (s *Store) DeleteAttachment(id string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM attachments WHERE id=?`, strings.TrimSpace(id))
	if err != nil {
		return false, fmt.Errorf("jobstore: delete attachment: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteUnsentAttachment removes a row only while it is still unsent, atomically with respect
// to BindAttachment: it returns ErrAttachmentNotFound for an unknown id and ErrAttachmentBound
// for a sent one.
func (s *Store) DeleteUnsentAttachment(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`DELETE FROM attachments WHERE id=? AND thread_id=''`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("jobstore: delete attachment: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, ok, gerr := s.GetAttachment(id); gerr != nil {
		return gerr
	} else if !ok {
		return ErrAttachmentNotFound
	}
	return ErrAttachmentBound
}

// ListAttachmentsByThread returns the attachments bound to a thread, oldest first.
func (s *Store) ListAttachmentsByThread(threadID string) ([]Attachment, error) {
	rows, err := s.db.Query(`SELECT `+attachmentCols+` FROM attachments WHERE thread_id=? ORDER BY created_at, id`,
		strings.TrimSpace(threadID))
	if err != nil {
		return nil, fmt.Errorf("jobstore: list attachments: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, fmt.Errorf("jobstore: scan attachment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneAttachments deletes every row whose expires_at is set and <= now (sent or not) and
// returns the stored paths (relative to the attachment root) so the caller can remove the
// files. Rows go first: a crash between the two leaves an orphan file, never a row pointing at
// nothing.
func (s *Store) PruneAttachments(now int64) ([]string, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.db.Query(`SELECT path FROM attachments WHERE expires_at>0 AND expires_at<=?`, now)
	if err != nil {
		return nil, fmt.Errorf("jobstore: prune attachments: %w", err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("jobstore: prune attachments: %w", err)
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(paths) == 0 {
		return nil, nil
	}
	if _, err := s.db.Exec(`DELETE FROM attachments WHERE expires_at>0 AND expires_at<=?`, now); err != nil {
		return nil, fmt.Errorf("jobstore: prune attachments: %w", err)
	}
	return paths, nil
}
