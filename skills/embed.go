// Package skills embeds the shipped agent skills so `gofer init skill` can
// install them from the binary — one source of truth, no drift between the
// tracked skills/ tree and what init writes out. The Go source lives at the
// skills/ root because //go:embed cannot reference parent directories, keeping
// skills/gofer-usage/ the single authoritative copy.
package skills

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// GoferUsage is the embedded gofer-usage/ skill tree (SKILL.md +
// references/*). The `all:` prefix pulls in any dot/underscore-prefixed files
// too, so the embedded copy always matches the on-disk tree verbatim.
//
//go:embed all:gofer-usage
var GoferUsage embed.FS

// SkillDirName is the top-level directory name inside GoferUsage; the installed
// skill lands at <destParent>/gofer-usage/.
const SkillDirName = "gofer-usage"

// InstallTo writes the embedded gofer-usage/ tree under destParent, preserving
// the directory structure (SKILL.md + references/commands.md). Directories are
// created 0755, files 0644. It returns the destination paths written (in walk
// order). It does NOT enforce an overwrite policy — the caller checks for an
// existing skill dir and honours --force before calling.
func InstallTo(destParent string) ([]string, error) {
	var written []string
	err := fs.WalkDir(GoferUsage, ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == "." {
			return nil
		}
		dest := filepath.Join(destParent, p)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, readErr := GoferUsage.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return err
		}
		written = append(written, dest)
		return nil
	})
	return written, err
}
