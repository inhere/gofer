package job

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// LoadEnvFilesMap loads declared dotenv-style files into a fresh map without
// mutating os.Environ. Paths are resolved by resolveEnvFilePath.
func LoadEnvFilesMap(paths []string, cfg *config.Config, proj config.ProjectConfig) (map[string]string, error) {
	out := map[string]string{}
	if len(paths) == 0 {
		return out, nil
	}
	loaded := make([]string, 0, len(paths))
	for _, declared := range paths {
		resolved, err := resolveEnvFilePath(declared, cfg, proj)
		if err != nil {
			return nil, err
		}
		values, err := parseEnvFile(resolved)
		if err != nil {
			return nil, err
		}
		for k, v := range values {
			out[k] = v
		}
		loaded = append(loaded, declared)
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	slog.Info("job env_files loaded", "files", loaded, "keys", keys)
	return out, nil
}

func resolveEnvFilePath(declared string, cfg *config.Config, proj config.ProjectConfig) (string, error) {
	name := strings.TrimSpace(declared)
	if name == "" {
		return "", fmt.Errorf("%w: env_files path must be non-empty", ErrInvalidRequest)
	}
	if err := rejectUnsafeEnvFilePath(name); err != nil {
		return "", err
	}

	normalized := strings.ReplaceAll(name, "\\", "/")
	cleaned := path.Clean(normalized)
	var root, rel string
	if isBareEnvFileName(cleaned) {
		dir, err := config.ConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve config dir for env_files: %w", err)
		}
		root, rel = dir, path.Join("secret", cleaned)
	} else if strings.HasPrefix(cleaned, "secret/") || cleaned == "secret" {
		dir, err := config.ConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve config dir for env_files: %w", err)
		}
		root, rel = dir, cleaned
	} else {
		root, rel = cfg.ExecPath(proj), cleaned
	}
	return safeExistingEnvFile(root, rel, name)
}

func rejectUnsafeEnvFilePath(name string) error {
	if filepath.IsAbs(name) || hasWindowsAbsEnvPath(name) {
		return fmt.Errorf("%w: env_files path %q must be relative", ErrInvalidRequest, name)
	}
	normalized := strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return fmt.Errorf("%w: env_files path %q must not contain '..'", ErrInvalidRequest, name)
		}
	}
	if cleaned := path.Clean(normalized); cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: env_files path %q escapes its root", ErrInvalidRequest, name)
	}
	return nil
}

func safeExistingEnvFile(root, rel, declared string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve env_files root: %w", err)
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(path.Clean(strings.ReplaceAll(rel, "\\", "/"))))
	if err := ensureUnder(rootAbs, full, declared); err != nil {
		return "", err
	}
	if err := rejectSymlinkPath(rootAbs, rel, declared); err != nil {
		return "", err
	}
	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: env_files file %q not found", ErrInvalidRequest, declared)
		}
		return "", fmt.Errorf("stat env_files file %q: %w", declared, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%w: env_files path %q is a directory", ErrInvalidRequest, declared)
	}
	if real, err := filepath.EvalSymlinks(full); err == nil {
		realRoot, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			realRoot = rootAbs
		}
		if err := ensureUnder(realRoot, real, declared); err != nil {
			return "", err
		}
	}
	return full, nil
}

func rejectSymlinkPath(root, rel, declared string) error {
	cleaned := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	cur := root
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("stat env_files path %q: %w", declared, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: env_files path %q must not contain symlinks", ErrInvalidRequest, declared)
		}
	}
	return nil
}

func ensureUnder(root, target, declared string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: env_files path %q escapes its root", ErrInvalidRequest, declared)
	}
	return nil
}

func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			return nil, fmt.Errorf("parse env file %s:%d: expected KEY=VALUE", path, lineNo)
		}
		key := strings.TrimSpace(line[:i])
		if !validEnvKey(key) {
			return nil, fmt.Errorf("parse env file %s:%d: invalid env key %q", path, lineNo, key)
		}
		out[key] = unquoteEnvValue(strings.TrimSpace(line[i+1:]))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	return out, nil
}

func unquoteEnvValue(v string) string {
	if len(v) >= 2 {
		q := v[0]
		if (q == '"' || q == '\'') && v[len(v)-1] == q {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func isBareEnvFileName(p string) bool {
	return !strings.Contains(p, "/") && !strings.Contains(p, string(filepath.Separator))
}

func hasWindowsAbsEnvPath(p string) bool {
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			return true
		}
	}
	return strings.HasPrefix(p, "\\") || strings.HasPrefix(p, "/")
}
