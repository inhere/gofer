// Package attachment stores the images a user uploads before sending them with a workbench
// message (design 2026-10-19-workbench-image-attachments-design.md 「附件存储（server）」).
//
// It owns the file side — type validation by magic bytes, dimensions, the on-disk layout
// <root>/<yyyymmdd>/<id>, thumbnails and the retention sweep — while the index rows live in
// jobstore (the attachments table). Ownership and thread-visibility rules are the HTTP
// layer's business; this package only knows ids and bytes.
package attachment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Errors a caller maps to HTTP statuses.
var (
	// ErrTooLarge: the upload exceeds Limits.MaxBytes (413).
	ErrTooLarge = errors.New("attachment: file too large")
	// ErrUnsupportedType: the bytes are not a PNG / JPEG / GIF / WebP image, or the image
	// header is damaged / absurdly large (415).
	ErrUnsupportedType = errors.New("attachment: unsupported or invalid image type")
	// ErrRateLimited: the caller exceeded Limits.HourlyPerCaller (429).
	ErrRateLimited = errors.New("attachment: hourly upload limit reached")
	// ErrNotFound: unknown id, or an id that cannot be one.
	ErrNotFound = errors.New("attachment: not found")
)

// Defaults of the server.attachments / storage.attachment_retention_days settings; the config
// package owns the yaml surface and passes the resolved numbers in through Limits.
const (
	DefaultMaxBytes        int64 = 10 << 20
	DefaultMaxPerMessage         = 6
	DefaultHourlyPerCaller       = 200
)

// Limits are the live numbers of one upload (read per call through the Service's limits func,
// so a config reload applies to the next upload).
type Limits struct {
	MaxBytes        int64
	HourlyPerCaller int
	// Retention is how long a new attachment is kept; <= 0 = keep forever (expires_at 0).
	Retention time.Duration
}

// Repo is the slice of jobstore.Store the service uses.
type Repo interface {
	InsertAttachment(a jobstore.Attachment) error
	GetAttachment(id string) (jobstore.Attachment, bool, error)
	DeleteUnsentAttachment(id string) error
	PruneAttachments(now int64) ([]string, error)
}

// Service is the attachment store.
type Service struct {
	root   string
	repo   Repo
	limits func() Limits
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time // caller -> upload attempts in the last hour
}

// New builds a Service rooted at root (the directory is created lazily on the first upload).
// limits is called per operation; nil takes the package defaults.
func New(root string, repo Repo, limits func() Limits) *Service {
	if limits == nil {
		limits = func() Limits {
			return Limits{MaxBytes: DefaultMaxBytes, HourlyPerCaller: DefaultHourlyPerCaller, Retention: 14 * 24 * time.Hour}
		}
	}
	return &Service{root: root, repo: repo, limits: limits, now: time.Now, hits: map[string][]time.Time{}}
}

// SetClock replaces the time source (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// MaxBytes returns the live upload ceiling (for the HTTP layer's body cap).
func (s *Service) MaxBytes() int64 {
	if n := s.limits().MaxBytes; n > 0 {
		return n
	}
	return DefaultMaxBytes
}

// Root returns the storage root.
func (s *Service) Root() string { return s.root }

// idPattern is the shape of every id this package mints; anything else never reaches the file
// system (no path separators, no dots).
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,40}$`)

// ValidID reports whether id looks like an attachment id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

func newID() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// allow records one upload attempt of caller and reports whether it is within the hourly limit.
// Attempts count whether or not the file turns out to be valid, and deleting an attachment does
// not give the quota back: the limit bounds the load a caller can put on the server.
func (s *Service) allow(caller string, limit int) bool {
	if limit <= 0 {
		return true
	}
	now := s.now()
	cut := now.Add(-time.Hour)
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.hits[caller][:0]
	for _, t := range s.hits[caller] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		s.hits[caller] = kept
		return false
	}
	s.hits[caller] = append(kept, now)
	return true
}

// Save validates and stores one upload from r for caller. Order of refusals: rate limit
// (ErrRateLimited), size (ErrTooLarge), type (ErrUnsupportedType). The returned row is
// unsent (no thread).
func (s *Service) Save(caller string, r io.Reader) (jobstore.Attachment, error) {
	lim := s.limits()
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = DefaultMaxBytes
	}
	if !s.allow(caller, lim.HourlyPerCaller) {
		return jobstore.Attachment{}, ErrRateLimited
	}
	data, err := io.ReadAll(io.LimitReader(r, lim.MaxBytes+1))
	if err != nil {
		return jobstore.Attachment{}, fmt.Errorf("attachment: read upload: %w", err)
	}
	if int64(len(data)) > lim.MaxBytes {
		return jobstore.Attachment{}, ErrTooLarge
	}
	mime := SniffMime(data)
	if mime == "" {
		return jobstore.Attachment{}, ErrUnsupportedType
	}
	w, h, ok := Dimensions(mime, data)
	if !ok {
		return jobstore.Attachment{}, ErrUnsupportedType
	}
	id, err := newID()
	if err != nil {
		return jobstore.Attachment{}, fmt.Errorf("attachment: new id: %w", err)
	}
	now := s.now()
	rel := filepath.ToSlash(filepath.Join(now.UTC().Format("20060102"), id))
	abs := filepath.Join(s.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return jobstore.Attachment{}, fmt.Errorf("attachment: create dir: %w", err)
	}
	if err := os.WriteFile(abs, data, 0o600); err != nil {
		return jobstore.Attachment{}, fmt.Errorf("attachment: write file: %w", err)
	}
	sum := sha256.Sum256(data)
	rec := jobstore.Attachment{
		ID: id, Caller: caller, Mime: mime, Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:]),
		Width: w, Height: h, CreatedAt: now.Unix(), Path: rel,
	}
	if lim.Retention > 0 {
		rec.ExpiresAt = now.Add(lim.Retention).Unix()
	}
	if err := s.repo.InsertAttachment(rec); err != nil {
		_ = os.Remove(abs)
		return jobstore.Attachment{}, err
	}
	return rec, nil
}

// Get reads the index row of id; ok is false for an unknown or malformed id.
func (s *Service) Get(id string) (jobstore.Attachment, bool, error) {
	if !ValidID(id) {
		return jobstore.Attachment{}, false, nil
	}
	return s.repo.GetAttachment(id)
}

// abs resolves a stored relative path under the root and refuses anything that escapes it.
func (s *Service) abs(rel string) (string, error) {
	p := filepath.Join(s.root, filepath.FromSlash(rel))
	r, err := filepath.Rel(s.root, p)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return "", fmt.Errorf("attachment: path %q is outside the storage root", rel)
	}
	return p, nil
}

// AbsPath is the absolute path of rec's stored original on this server (the source of a file
// delivery); it refuses a stored path that escapes the root.
func (s *Service) AbsPath(rec jobstore.Attachment) (string, error) { return s.abs(rec.Path) }

// Read returns the stored original of rec. A row whose file is gone (pruned by hand, expired
// mid-request) is ErrNotFound.
func (s *Service) Read(rec jobstore.Attachment) ([]byte, error) {
	p, err := s.abs(rec.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

// ReadThumb returns the thumbnail of rec (see Thumbnail; any failure returns the original).
func (s *Service) ReadThumb(rec jobstore.Attachment) (data []byte, mime string, err error) {
	orig, err := s.Read(rec)
	if err != nil {
		return nil, "", err
	}
	data, mime, _ = Thumbnail(rec.Mime, orig)
	return data, mime, nil
}

// DeleteUnsent removes an unsent attachment (row first, then file). The repo returns
// jobstore.ErrAttachmentBound / ErrAttachmentNotFound unchanged.
func (s *Service) DeleteUnsent(rec jobstore.Attachment) error {
	if err := s.repo.DeleteUnsentAttachment(rec.ID); err != nil {
		return err
	}
	if p, err := s.abs(rec.Path); err == nil {
		_ = os.Remove(p)
	}
	return nil
}

// Prune deletes the rows expired at now() and their files, then removes day directories that
// became empty. It returns the number of attachments removed. A file that cannot be removed is
// skipped (its row is already gone; the next sweep does not see it again, so it is reported in
// the error but never blocks the rest).
func (s *Service) Prune() (int, error) {
	paths, err := s.repo.PruneAttachments(s.now().Unix())
	if err != nil {
		return 0, err
	}
	var firstErr error
	dirs := map[string]struct{}{}
	for _, rel := range paths {
		p, perr := s.abs(rel)
		if perr != nil {
			if firstErr == nil {
				firstErr = perr
			}
			continue
		}
		if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && firstErr == nil {
			firstErr = rerr
		}
		dirs[filepath.Dir(p)] = struct{}{}
	}
	for d := range dirs {
		// os.Remove on a non-empty directory fails, which is exactly the guard we want.
		if d != s.root {
			_ = os.Remove(d)
		}
	}
	return len(paths), firstErr
}
