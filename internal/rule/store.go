package rule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/template"
)

// ruleExt is the file extension of one stored rule: <root>/<name>.md.
const ruleExt = ".md"

// Rule is one library entry's INDEX row: what `agent rule ls` lists, what the
// binding resolver validates a name against, and what a job row records as the
// version it injected. The text itself stays on disk (see Store.Read / Store.Body),
// exactly like a skill's files.
//
// The optional `agents:` frontmatter key a rule may carry is a human-facing hint
// ("this one is for the codex agent") and is deliberately NOT part of the index: it
// changes no behaviour, and an index column that nothing reads is a column that
// drifts.
type Rule struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Size        int64  `json:"size,omitempty"`
	// SHA256 is the digest of the stored file VERBATIM (frontmatter included), which
	// is what a job row records as "the version that was injected" — two rules whose
	// bytes differ never share a digest, so `job show` can answer "which text did this
	// run actually carry?" against the library as it is today.
	SHA256    string `json:"sha256,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// The failures a caller can act on. Everything else is an I/O error wrapped with
// context. They are separate sentinels because the HTTP/CLI surfaces map them to
// different statuses/messages.
var (
	ErrNotFound = errors.New("rule not found")
	ErrInvalid  = errors.New("invalid rule")
)

// nameRe is the rule name grammar: lower-case alphanumerics and dashes, opening with
// an alphanumeric so neither "." nor ".." nor a hidden transient file under the root
// can ever be a name. It matches the skill grammar's safe subset (design §一.1: 名字
// 规则同 skill).
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Repo is the rule index: one row per rule. It is an interface so the library can be
// tested (and stored) without SQLite; internal/jobstore implements the same column
// set.
type Repo interface {
	UpsertRule(Rule) error
	GetRule(name string) (Rule, bool, error)
	ListRules() ([]Rule, error)
	DeleteRule(name string) error
}

// Store is the rule library: a root directory of <name>.md files plus an index.
type Store struct {
	root string
	repo Repo
}

// NewStore opens (creating if needed) the library at root. Callers pass
// <config-dir>/rules so the library is backed up with the config (design §一.1).
func NewStore(root string, repo Repo) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: empty rule library root", ErrInvalid)
	}
	if repo == nil {
		return nil, fmt.Errorf("%w: the rule library needs an index", ErrInvalid)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("rule: create library %s: %w", root, err)
	}
	return &Store{root: root, repo: repo}, nil
}

// Root is the library root, absolute when the caller passed one.
func (s *Store) Root() string { return s.root }

// Path is the on-disk file of one rule. It returns "" for a name that fails
// validation, so a caller that ignores the index (or a typo) can never be handed a
// path outside the library root.
func (s *Store) Path(name string) string {
	if validateName(name) != nil {
		return ""
	}
	return filepath.Join(s.root, name+ruleExt)
}

// List returns every indexed rule, in the index's order.
func (s *Store) List() ([]Rule, error) { return s.repo.ListRules() }

// Get returns the indexed rule. The bool is false (with a nil error) when the
// library has no such rule.
func (s *Store) Get(name string) (Rule, bool, error) {
	if err := validateName(name); err != nil {
		return Rule{}, false, err
	}
	return s.repo.GetRule(name)
}

// Read returns the rule's file verbatim, frontmatter included (what `rule show`
// prints). A valid name with no file is ErrNotFound: the index and the tree
// disagree, and the caller must not silently get an empty rule.
func (s *Store) Read(name string) (string, error) {
	p := s.Path(name)
	if p == "" {
		return "", fmt.Errorf("%w: bad rule name %q", ErrInvalid, name)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return "", fmt.Errorf("rule: read %s: %w", p, err)
	}
	return string(b), nil
}

// Body returns the text that is INJECTED into a prompt: the file with its optional
// YAML frontmatter stripped, so the metadata head never reaches the agent twice.
func (s *Store) Body(name string) (string, error) {
	raw, err := s.Read(name)
	if err != nil {
		return "", err
	}
	return BodyOf(raw), nil
}

// BodyOf strips the optional `---`-delimited YAML frontmatter from rule text and
// trims the result. A file without a head (or with a malformed one) is all body.
func BodyOf(raw string) string {
	if _, rest, ok := template.SplitFrontmatter([]byte(raw)); ok {
		return strings.TrimSpace(string(rest))
	}
	return strings.TrimSpace(raw)
}

// Set writes <name>.md (atomically: temp file + rename, so a crash never leaves a
// half-written rule the index points at) and then updates the index row. A
// RE-SET of an existing name replaces it — that is what `agent rule set` is for.
//
// The file is written first and the index second because the index is what the
// binding resolver validates against: a crash between the two leaves a file that no
// binding can name yet (harmless, repaired by the next Set), never an index row
// pointing at a missing file.
func (s *Store) Set(name string, content []byte, caller string) (Rule, error) {
	if err := validateName(name); err != nil {
		return Rule{}, err
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return Rule{}, fmt.Errorf("%w: rule %q has no body", ErrInvalid, name)
	}
	p := s.Path(name)
	if err := writeAtomic(p, content); err != nil {
		return Rule{}, err
	}
	sum := sha256.Sum256(content)
	r := Rule{
		Name:        name,
		Description: descriptionOf(content),
		Size:        int64(len(content)),
		SHA256:      hex.EncodeToString(sum[:]),
		UpdatedAt:   time.Now().Unix(),
		UpdatedBy:   caller,
	}
	if err := s.repo.UpsertRule(r); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Remove deletes the index row first and the file second — the same order (and the
// same reason) as the skill store: a crash between the two steps leaves an orphan
// file that is invisible to `rule ls` and reused by the next Set of the same name,
// instead of an index row pointing at a file that is gone.
func (s *Store) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if _, ok, err := s.repo.GetRule(name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err := s.repo.DeleteRule(name); err != nil {
		return err
	}
	if p := s.Path(name); p != "" {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rule: remove %s: %w", p, err)
		}
	}
	return nil
}

// validateName enforces the name grammar.
func validateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w: %q is not a rule name (lower-case letters, digits and '-', starting with a letter or digit)", ErrInvalid, name)
	}
	return nil
}

// descriptionOf reads the optional frontmatter's description. A malformed head is
// not an error — the description is a convenience, the body is the rule.
func descriptionOf(content []byte) string {
	fm, _, ok := template.SplitFrontmatter(content)
	if !ok {
		return ""
	}
	var head struct {
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(fm, &head); err != nil {
		return ""
	}
	return strings.TrimSpace(head.Description)
}

// writeAtomic writes data to path through a sibling temp file and renames it on
// success, removing the temp file on any failure. Directories are 0755, the file
// 0644: a rule is text, never a program.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("rule: create temp beside %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("rule: write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rule: write %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rule: chmod %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rule: install %s: %w", path, err)
	}
	return nil
}
