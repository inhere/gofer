package skill

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// Import copies a source into the library under the name its SKILL.md declares
// (or the source's own basename) and indexes it. It accepts a local directory, a
// local .zip, an http(s) URL of a .zip, and git+https://…[#subdir] (the last two
// are fetched server-side; design §一.2).
//
// Importing a name that already exists REPLACES it — that is the whole point of
// `skill import` being re-runnable — and the replacement is atomic in the sense
// that matters: the new tree is built and validated off to the side and only then
// swapped in (see publish), so a source that fails validation (no SKILL.md,
// escape, symlink, over the caps) leaves the previous skill exactly as it was.
func (s *Store) Import(src, caller string) (Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, err := s.prepare(src, "", caller)
	if err != nil {
		return Skill{}, err
	}
	defer os.RemoveAll(st.tmp)

	_, exists, err := s.repo.GetSkill(st.name)
	if err != nil {
		return Skill{}, fmt.Errorf("skill: index lookup %q: %w", st.name, err)
	}
	return s.publish(st, !exists)
}

// Update re-fetches a skill from the source its index entry recorded and replaces
// the library copy, returning the file-level diff (by sha256). A skill imported
// before the source was recorded — or from a source that no longer resolves —
// cannot be updated and says so with ErrInvalid.
func (s *Store) Update(name, caller string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok, err := s.Get(name)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, fmt.Errorf("%w: skill %q", ErrNotFound, name)
	}
	if strings.TrimSpace(cur.SourceRef) == "" {
		return Change{}, fmt.Errorf(
			"%w: skill %q has no recorded source (source=%q); import it again to make it updatable",
			ErrInvalid, name, cur.Source)
	}

	st, err := s.prepare(cur.SourceRef, name, caller)
	if err != nil {
		return Change{}, err
	}
	defer os.RemoveAll(st.tmp)

	// The library is keyed by name, so a source that renamed itself must not
	// silently move the entry (or leave the old directory behind). The user decides.
	if st.name != name {
		return Change{}, fmt.Errorf(
			"%w: the source of %q now declares name %q; remove %q and import it again",
			ErrInvalid, name, st.name, name)
	}
	ch := diffFiles(cur.Files, st.files)
	ch.Name = name
	if _, err := s.publish(st, false); err != nil {
		return Change{}, err
	}
	return ch, nil
}

// prepare materialises spec into a fresh temp dir under the store root, validates
// it as a skill and returns it staged. It is the single import pipeline: Import
// passes the caller's spec, Update passes the spec the index recorded.
//
// The temp dir sits under the store root so the final move into <root>/<name> is a
// rename on one filesystem, and it is removed on every failure path — the library
// only ever sees a complete skill.
func (s *Store) prepare(spec, fallbackName, caller string) (staged, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return staged{}, fmt.Errorf("%w: empty skill source", ErrInvalid)
	}
	tmp, err := os.MkdirTemp(s.root, tmpPrefix)
	if err != nil {
		return staged{}, fmt.Errorf("skill: staging dir under %s: %w", s.root, err)
	}
	st := staged{tmp: tmp, caller: caller}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}()

	local, kind, ref, err := fetchSource(spec, tmp, s.limits)
	if err != nil {
		return staged{}, err
	}
	fi, err := os.Stat(local)
	if err != nil {
		return staged{}, fmt.Errorf("skill: source %q: %w", spec, err)
	}
	src := filepath.Join(tmp, "skill")
	if fi.IsDir() {
		if _, err := copyTree(src, local, s.limits); err != nil {
			return staged{}, err
		}
	} else {
		if err := extractZip(src, local, s.limits); err != nil {
			return staged{}, err
		}
	}

	dir, err := unwrapRoot(src)
	if err != nil {
		return staged{}, err
	}
	if fallbackName == "" {
		fallbackName = defaultNameFor(spec, kind, local)
	}
	name, desc, err := readManifest(dir, fallbackName)
	if err != nil {
		return staged{}, err
	}
	files, size, err := computeFiles(dir)
	if err != nil {
		return staged{}, err
	}

	st.dir, st.name, st.desc = dir, name, desc
	st.files, st.size = files, size
	st.source, st.ref = kind, ref
	keep = true
	return st, nil
}

// unwrapRoot descends into a single wrapper directory. Archives built from a
// directory ("Download ZIP", `zip -r skill.zip skill`) wrap everything in one
// top-level dir, and SKILL.md is required AT the skill root, so without this an
// import of such a zip would be rejected as "no SKILL.md" even though it is
// obviously a skill. Only a lone directory counts — a tree with a README next to
// the real dir is ambiguous and stays as-is (so it fails the manifest check loudly
// instead of importing the wrong level).
func unwrapRoot(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, manifestName)); err == nil {
			return dir, nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", fmt.Errorf("skill: read staged %s: %w", dir, err)
		}
		if len(entries) != 1 || !entries[0].IsDir() {
			return dir, nil
		}
		dir = filepath.Join(dir, entries[0].Name())
	}
}

// extractZip unpacks a .zip into dst, enforcing the same rules as copyTree plus
// one more: the entry name is attacker-chosen text. Every entry goes through
// safeJoin before anything is written, symlinks and non-regular entries are
// refused, and the byte caps are applied to the DECOMPRESSED bytes actually read
// (a zip may lie about its sizes, so the header is never trusted).
func extractZip(dst, zipPath string, lim Limits) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("skill: open %s: %w", zipPath, err)
	}
	defer zr.Close()

	var total int64
	for _, f := range zr.File {
		name := f.Name
		if name == "" || name == "." || name == "./" {
			continue // the archive's root marker
		}
		if f.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, name)
		}
		out, err := safeJoin(dst, name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return fmt.Errorf("skill: mkdir %s: %w", name, err)
			}
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrInvalid, name)
		}
		n, err := extractZipFile(out, f, lim, name)
		if err != nil {
			return err
		}
		total += n
		if total > lim.MaxTotalBytes {
			return totalErr(name, lim.MaxTotalBytes)
		}
	}
	return nil
}

// extractZipFile writes one archive member, capped at lim.MaxFileBytes.
func extractZipFile(dst string, f *zip.File, lim Limits, shown string) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("skill: read %s: %w", shown, err)
	}
	defer rc.Close()
	// One byte past the cap: "exactly max_file_bytes" still imports, one more
	// fails, and the check is on real bytes rather than f.UncompressedSize64.
	data, err := io.ReadAll(io.LimitReader(rc, lim.MaxFileBytes+1))
	if err != nil {
		return 0, fmt.Errorf("skill: read %s: %w", shown, err)
	}
	if int64(len(data)) > lim.MaxFileBytes {
		return 0, fmt.Errorf("%w: %s is larger than max_file_bytes (%d)", ErrTooLarge, shown, lim.MaxFileBytes)
	}
	if err := writeFile(dst, data); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

// readManifest reads the required SKILL.md and resolves the skill's name: the
// frontmatter's `name` when present, else the fallback the caller derived from the
// source (a directory basename, a zip/URL basename, a repo or #subdir name).
func readManifest(dir, fallback string) (string, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("%w: %s has no %s (a skill is a directory with a %s at its root)",
				ErrInvalid, dir, manifestName, manifestName)
		}
		return "", "", fmt.Errorf("skill: read %s: %w", filepath.Join(dir, manifestName), err)
	}
	name, desc := frontmatter(data)
	if name == "" {
		name = fallback
	}
	if err := validateName(name); err != nil {
		return "", "", err
	}
	return name, strings.TrimSpace(desc), nil
}

// frontmatter reads the optional `---`-delimited YAML head of SKILL.md and returns
// its name/description. Malformed or absent frontmatter is NOT an error — the head
// is a convenience, the file body is the skill — so every failure path here
// returns empty strings and lets the caller fall back to the source's name.
func frontmatter(data []byte) (name, description string) {
	// A UTF-8 BOM does not stop a text editor from showing "---" at the top, so it
	// must not stop us either.
	s := strings.TrimPrefix(string(data), "\uFEFF")
	if !strings.HasPrefix(s, "---") {
		return "", ""
	}
	nl := strings.IndexByte(s, '\n')
	if nl < 0 || strings.TrimSpace(s[:nl]) != "---" {
		return "", ""
	}
	body := s[nl+1:]
	// The closing delimiter is a line that is exactly "---"; without one there is
	// no frontmatter (the whole file is body text).
	end := -1
	for i := 0; i <= len(body); {
		line := body[i:]
		if j := strings.IndexByte(line, '\n'); j >= 0 {
			line = line[:j]
		}
		if strings.TrimSpace(line) == "---" {
			end = i
			break
		}
		j := strings.IndexByte(body[i:], '\n')
		if j < 0 {
			break
		}
		i += j + 1
	}
	if end < 0 {
		return "", ""
	}
	var head struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(body[:end]), &head); err != nil {
		return "", ""
	}
	return strings.TrimSpace(head.Name), strings.TrimSpace(head.Description)
}

// hasZipExt reports a .zip suffix on a local path or a URL path (query/fragment
// stripped), case-insensitively.
func hasZipExt(p string) bool {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	return strings.HasSuffix(strings.ToLower(strings.ReplaceAll(p, "\\", "/")), ".zip")
}
