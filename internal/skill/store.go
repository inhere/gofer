package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/util"
)

const (
	// manifestName is the one file a skill cannot be without: it carries the
	// name/description frontmatter and is the entry point the mounted prompt
	// points the agent at.
	manifestName = "SKILL.md"
	// tmpPrefix / replacedSuffix name the transient directories under the store
	// root. Both start with a dot, so even a leftover from a crash (a kill between
	// the two renames) is neither a legal skill name (nameRe) nor picked up by
	// anything that walks the root.
	tmpPrefix      = ".tmp-"
	replacedSuffix = ".replaced-"
)

// File is one file of a skill, path relative to the skill dir, slash-separated.
// SHA256 is what lets `skill update` report "these files changed" without shipping
// bytes, and what the version hash is computed over.
type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// Skill is one library entry: the metadata the index keeps, plus the file list the
// import was validated with. The bytes themselves live on disk (Store.Dir); this
// shadow copy is what makes listing and diffing possible without walking the tree.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`     // dir|zip|url|git — what it came from
	SourceRef   string `json:"source_ref,omitempty"` // the spec Update re-fetches verbatim
	Version     string `json:"version,omitempty"`    // sha256 over the file list
	Files       []File `json:"files,omitempty"`
	Size        int64  `json:"size,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"`
	UpdatedBy   string `json:"updated_by,omitempty"`
}

// Repo is the skill index: five statements over one row per skill. It is an
// interface so the library can be tested (and stored) without SQLite; jobstore
// implements the same column set behind JobstoreRepo.
type Repo interface {
	InsertSkill(Skill) error
	UpdateSkill(Skill) error
	GetSkill(name string) (Skill, bool, error)
	ListSkills() ([]Skill, error)
	DeleteSkill(name string) error
}

// Limits caps what a single skill may contain. They are checked while a source is
// being read (not against the size fields an archive reports about itself, which
// are attacker-controlled), so an oversized source is refused before it can fill
// the disk.
type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits is the shipped 2MiB per file / 10MiB per skill: a skill is prompt
// material, and past those sizes an agent cannot read it in one go anyway.
func DefaultLimits() Limits { return Limits{MaxFileBytes: 2 << 20, MaxTotalBytes: 10 << 20} }

// withDefaults fills a partially specified Limits from DefaultLimits: zero reads
// as "unset" (the way every other cap in gofer's config behaves), so a caller can
// configure only the field it cares about.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = d.MaxTotalBytes
	}
	return l
}

// The failures a caller can act on. Everything else is an I/O error wrapped with
// context. They are separate sentinels because the CLI maps them to different
// exits/messages: "wrong name" and "the source is not a skill" are user errors,
// while ErrEscape/ErrSymlink/ErrTooLarge are a refused import.
var (
	ErrNotFound = errors.New("skill: not found")
	ErrInvalid  = errors.New("skill: invalid skill")
	ErrEscape   = errors.New("skill: path escapes the skill dir")
	ErrSymlink  = errors.New("skill: symlinks are not allowed")
	ErrTooLarge = errors.New("skill: over the size limits")
)

// nameRe is the skill name grammar: lower-case, dot/dash/underscore, and it opens
// with an alphanumeric so neither "." nor ".." nor a hidden transient directory
// under the store root can ever be a name.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Store is the skill library: a root directory of skill dirs plus an index.
//
// mu serialises every operation that reads or replaces a skill's tree. Import and
// Update are read-modify-write over both the filesystem and the index, and holding
// the lock for the whole operation is what makes "replace atomically" true; these
// are admin operations, so the cost of serialising them (a git clone under the
// lock) buys correctness we cannot get any other way.
type Store struct {
	root   string
	repo   Repo
	limits Limits
	mu     sync.Mutex
}

// NewStore opens (creating if needed) the library at root. Callers pass
// <config-dir>/skills so the library is backed up with the config (design §一.1).
func NewStore(root string, repo Repo, limits Limits) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: empty store root", ErrInvalid)
	}
	if repo == nil {
		return nil, fmt.Errorf("%w: nil skill index", ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("skill: resolve root %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("skill: create root %s: %w", abs, err)
	}
	return &Store{root: abs, repo: repo, limits: limits.withDefaults()}, nil
}

// Root is the library root, absolute.
func (s *Store) Root() string { return s.root }

// Dir is the on-disk directory of one skill. It returns "" for a name that fails
// validation, so a caller that ignores the index (or a typo) can never be handed a
// path outside the store root.
func (s *Store) Dir(name string) string {
	if validateName(name) != nil {
		return ""
	}
	return filepath.Join(s.root, name)
}

// Get returns the indexed skill. The bool is false (with a nil error) when the
// library has no such skill.
func (s *Store) Get(name string) (Skill, bool, error) {
	if err := validateName(name); err != nil {
		return Skill{}, false, err
	}
	sk, ok, err := s.repo.GetSkill(name)
	if err != nil {
		return Skill{}, false, fmt.Errorf("skill: load %q: %w", name, err)
	}
	return sk, ok, nil
}

// List returns every indexed skill, in the index's order.
func (s *Store) List() ([]Skill, error) {
	out, err := s.repo.ListSkills()
	if err != nil {
		return nil, fmt.Errorf("skill: list: %w", err)
	}
	return out, nil
}

// Remove deletes the index row first and the directory second. That order is
// deliberate: the index is what `skill ls` and the binding resolver read, so a
// crash between the two steps leaves an orphan directory (invisible, reused by the
// next import of the same name) instead of an index row pointing at files that are
// gone.
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateName(name); err != nil {
		return err
	}
	if _, ok, err := s.repo.GetSkill(name); err != nil {
		return fmt.Errorf("skill: load %q: %w", name, err)
	} else if !ok {
		return fmt.Errorf("%w: skill %q", ErrNotFound, name)
	}
	if err := s.repo.DeleteSkill(name); err != nil {
		return fmt.Errorf("skill: index %q: %w", name, err)
	}
	if err := os.RemoveAll(filepath.Join(s.root, name)); err != nil {
		return fmt.Errorf("skill: remove %s: %w", filepath.Join(s.root, name), err)
	}
	return nil
}

// validateName enforces the name grammar.
func validateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w: name %q must match %s", ErrInvalid, name, nameRe.String())
	}
	return nil
}

// dirOf validates a name and returns its existing directory; a name that is valid
// but absent from disk is ErrNotFound (the index and the tree disagree — the
// caller must not silently get an empty directory).
func (s *Store) dirOf(name string) (string, error) {
	dir := s.Dir(name)
	if dir == "" {
		return "", fmt.Errorf("%w: name %q", ErrInvalid, name)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: skill %q has no directory in the library", ErrNotFound, name)
	}
	return dir, nil
}

// computeFiles hashes every file under dir and returns the index the store
// persists: slash-separated path relative to the skill dir, its sha256 and its
// size, plus the total size. Sorted by path so two identical trees always produce
// the same Version (a directory walk is only lexicographic per level).
func computeFiles(dir string) ([]File, int64, error) {
	var files []File
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		sum, size, herr := hashFile(p)
		if herr != nil {
			return herr
		}
		files = append(files, File{Path: filepath.ToSlash(rel), SHA256: sum, Size: size})
		total += size
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("skill: index %s: %w", dir, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, total, nil
}

// hashFile returns the hex sha256 and the byte length of one file.
func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, fmt.Errorf("skill: hash %s: %w", p, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("skill: hash %s: %w", p, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// versionOf is the skill version: the sha256 of the "<path>\x00<sha256>\n" lines
// taken in path order (design §一.1). It is a content hash of the whole file list,
// so two imports whose bytes differ never share a version — which is what makes it
// usable as the "did this skill change?" answer without a second scan.
func versionOf(files []File) string {
	h := sha256.New()
	var line []byte
	for _, f := range files {
		line = line[:0]
		line = append(line, f.Path...)
		line = append(line, 0)
		line = append(line, f.SHA256...)
		line = append(line, '\n')
		// hash.Hash never fails; the empty error is part of the Write contract.
		_, _ = h.Write(line)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// staged is a fully materialised, validated skill sitting in a temp dir under the
// store root: everything publish needs, and nothing that touches the library yet.
type staged struct {
	tmp    string // the temp dir to drop once the skill dir has been moved out
	dir    string // the skill root inside tmp
	name   string
	desc   string
	source string
	ref    string
	files  []File
	size   int64
	caller string
}

// publish moves a staged skill into the library and writes its index row.
//
// The move is a rename onto the target, with the previous tree parked at a sibling
// path (replacedSuffix) until the index write succeeded: if the rename fails the
// old skill is put back, and if the index write fails the new tree is dropped and
// the old one restored. Files and index therefore never disagree, which is the
// whole point of staging (design §一.2: a failed import never leaves a half-written
// skill behind).
func (s *Store) publish(st staged, insert bool) (Skill, error) {
	dst := filepath.Join(s.root, st.name)
	prev, err := swapDir(dst, st.dir)
	if err != nil {
		return Skill{}, err
	}
	meta := Skill{
		Name:        st.name,
		Description: st.desc,
		Source:      st.source,
		SourceRef:   st.ref,
		Version:     versionOf(st.files),
		Files:       st.files,
		Size:        st.size,
		UpdatedAt:   time.Now().Unix(),
		UpdatedBy:   st.caller,
	}
	var werr error
	if insert {
		werr = s.repo.InsertSkill(meta)
	} else {
		werr = s.repo.UpdateSkill(meta)
	}
	if werr != nil {
		_ = os.RemoveAll(dst)
		if prev != "" {
			_ = os.Rename(prev, dst)
		}
		return Skill{}, fmt.Errorf("skill: index %q: %w", st.name, werr)
	}
	if prev != "" {
		_ = os.RemoveAll(prev)
	}
	return meta, nil
}

// swapDir moves src onto dst and returns the parked previous tree ("" when dst did
// not exist). A failed install restores the previous tree, so dst is never left
// missing by a failure.
func swapDir(dst, src string) (string, error) {
	prev := ""
	if _, err := os.Lstat(dst); err == nil {
		prev = dst + replacedSuffix + strconv.FormatInt(time.Now().UnixNano(), 36)
		if err := os.Rename(dst, prev); err != nil {
			return "", fmt.Errorf("skill: park %s: %w", filepath.Base(dst), err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("skill: stat %s: %w", dst, err)
	}
	if err := os.Rename(src, dst); err != nil {
		if prev != "" {
			_ = os.Rename(prev, dst)
		}
		return "", fmt.Errorf("skill: install %s: %w", filepath.Base(dst), err)
	}
	return prev, nil
}

// copyTree copies a directory tree into dst (parents created), preserving the
// relative layout and enforcing lim. It is used both to lift a source dir into the
// staging area and to Mount a skill into a job's private directory.
//
// The copy is a fresh write with mode 0644/0755, never a mode-preserving copy: the
// exec bit is stripped on the way in (design §一.2). Symlinks are refused rather
// than followed — a link is how an untrusted tree reaches outside itself — and
// anything that is not a regular file or a directory is refused as invalid.
func copyTree(dst, src string, lim Limits) (int64, error) {
	var total int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		shown := filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, shown)
		}
		out, jerr := safeJoin(dst, shown)
		if jerr != nil {
			return jerr
		}
		if d.IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return fmt.Errorf("skill: mkdir %s: %w", shown, err)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrInvalid, shown)
		}
		n, cerr := copyFile(out, p, lim, shown)
		if cerr != nil {
			return cerr
		}
		total += n
		if total > lim.MaxTotalBytes {
			return totalErr(shown, lim.MaxTotalBytes)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// copyFile writes one file, capped at lim.MaxFileBytes. The cap is applied to the
// bytes actually read (a LimitReader one byte past the cap, so "exactly at the
// cap" still succeeds and anything larger is a definite ErrTooLarge naming the
// file).
func copyFile(dst, src string, lim Limits, shown string) (int64, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("skill: read %s: %w", src, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, lim.MaxFileBytes+1))
	if err != nil {
		return 0, fmt.Errorf("skill: read %s: %w", src, err)
	}
	if int64(len(data)) > lim.MaxFileBytes {
		return 0, fmt.Errorf("%w: %s is larger than max_file_bytes (%d)", ErrTooLarge, shown, lim.MaxFileBytes)
	}
	if err := writeFile(dst, data); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

// writeFile creates the file and its parents with the skill's modes: 0755 for
// directories (traversable), 0644 for files (never executable).
func writeFile(dst string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("skill: mkdir %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("skill: write %s: %w", dst, err)
	}
	return nil
}

// totalErr is the shared "this file pushed the skill over the total cap" error; the
// offending path is named because a wide import is otherwise impossible to debug.
func totalErr(shown string, max int64) error {
	return fmt.Errorf("%w: %s pushes the skill over max_total_bytes (%d)", ErrTooLarge, shown, max)
}

// safeJoin resolves one UNTRUSTED relative path (an archive entry name, or a path
// relative to a fetched tree) against root and guarantees the result stays inside
// it. root must be absolute.
//
// It is a deliberate local copy of internal/project.SafeJoin's guarantee rather
// than a call to it: internal/project exists to resolve a *project's* exec root and
// drags internal/config (and its acp/tunnel imports) in behind it, which a data
// package has no business depending on. The error is ErrEscape — not a message
// about a "cwd" — because that is the failure the caller reports to the user.
//
// The absolute-name check runs on the SLASH form, before the platform touches it:
// filepath.IsAbs("/x") is false on Windows, and a zip made on one platform is
// routinely extracted on the other, so "/x" and "C:/x" are refused everywhere.
func safeJoin(root, name string) (string, error) {
	escape := func() (string, error) { return "", fmt.Errorf("%w: %q", ErrEscape, name) }
	slash := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(slash, "/") || path.IsAbs(slash) || hasWindowsDrive(slash) {
		return escape()
	}
	// "." is the root itself (an archive's "./" marker): the caller skips it, and
	// resolving anything else here would hand out the root as a file path.
	clean := path.Clean(slash)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return escape()
	}
	dst := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, dst)
	if err != nil || filepath.IsAbs(rel) || rel == "." ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return escape()
	}
	return dst, nil
}

// hasWindowsDrive reports a `X:` prefix, which filepath.IsAbs only rejects on
// Windows itself.
func hasWindowsDrive(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// within reports whether b is a itself or lives inside a.
func within(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// absPath resolves a caller-supplied destination directory; empty is refused
// rather than silently becoming the process working directory.
func absPath(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("%w: empty directory", ErrInvalid)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("skill: resolve %s: %w", dir, err)
	}
	return abs, nil
}

// diffFiles compares two file indexes by path and sha256: a path only upstream is
// Removed, only locally Added, and present on both sides with different bytes
// Changed. Size is NOT the criterion — a same-length edit is a change.
func diffFiles(old, cur []File) Change {
	// Capacity: at most one entry per file of the union of both sides.
	capHint := util.CapSum(len(old), len(cur))
	ch := Change{
		Added:   make([]string, 0, capHint),
		Changed: make([]string, 0, capHint),
		Removed: make([]string, 0, capHint),
	}
	oldSum := make(map[string]string, util.CapSum(len(old), 1))
	for _, f := range old {
		oldSum[f.Path] = f.SHA256
	}
	curPaths := make(map[string]struct{}, util.CapSum(len(cur), 1))
	for _, f := range cur {
		curPaths[f.Path] = struct{}{}
		if sum, ok := oldSum[f.Path]; !ok {
			ch.Added = append(ch.Added, f.Path)
		} else if sum != f.SHA256 {
			ch.Changed = append(ch.Changed, f.Path)
		}
	}
	for _, f := range old {
		if _, ok := curPaths[f.Path]; !ok {
			ch.Removed = append(ch.Removed, f.Path)
		}
	}
	return ch
}

// Change is what a re-fetch did to a skill: the paths added, changed and removed
// by the update, each in path order.
type Change struct {
	Name    string
	Added   []string
	Changed []string
	Removed []string
}
