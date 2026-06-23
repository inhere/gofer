package project

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// SafeJoin resolves a request-supplied relative cwd against a project's
// execution root (execRoot, = Config.ExecPath(proj), E29/D10) and guarantees the
// result stays inside that root. Only relative cwd values are accepted; an empty
// cwd is treated as ".".
//
// It returns the absolute directory (under execRoot) to run in.
//
// The code runs on Linux but must reject Windows-style absolute inputs too
// (drive letters like `D:\x` / `C:/y`, and backslash-rooted `\x`), since the
// caller may be a host on Windows. We therefore do manual detection instead of
// relying solely on the platform's filepath.IsAbs.
func SafeJoin(execRoot string, cwd string) (string, error) {
	if cwd == "" {
		cwd = "."
	}
	if err := assertRelative(cwd); err != nil {
		return "", err
	}

	hostAbs, err := filepath.Abs(execRoot)
	if err != nil {
		return "", fmt.Errorf("resolve exec_path: %w", err)
	}

	// Normalize backslashes so Windows-style relative inputs (a\b) are treated
	// as path separators on Linux too, then clean and join.
	normalized := strings.ReplaceAll(cwd, "\\", "/")
	joined := filepath.Join(hostAbs, filepath.Clean(normalized))

	rel, err := filepath.Rel(hostAbs, joined)
	if err != nil {
		return "", fmt.Errorf("cwd %q escapes project root", cwd)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("cwd %q escapes project root", cwd)
	}
	return joined, nil
}

// assertRelative rejects absolute cwd values in both POSIX and Windows forms.
func assertRelative(cwd string) error {
	if filepath.IsAbs(cwd) {
		return fmt.Errorf("cwd %q must be relative", cwd)
	}
	// Windows drive letter absolute, e.g. D:\x or C:/y.
	if hasWindowsDrive(cwd) {
		return fmt.Errorf("cwd %q must be relative (windows drive path rejected)", cwd)
	}
	// Backslash-rooted absolute, e.g. \x or \\server\share.
	if strings.HasPrefix(cwd, "\\") {
		return fmt.Errorf("cwd %q must be relative (windows root path rejected)", cwd)
	}
	return nil
}

// hasWindowsDrive reports whether s starts with a `X:` drive prefix.
func hasWindowsDrive(s string) bool {
	if len(s) < 2 || s[1] != ':' {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ExchangeDir returns the absolute exchange directory for a project:
// <exec_path>/<resolved exchange_subdir> (exec_path = Config.ExecPath, E29/D10).
// The exchange subdir is validated to stay inside the project root.
func ExchangeDir(cfg *config.Config, proj config.ProjectConfig) (string, error) {
	sub := cfg.ResolvedExchangeSubdir(proj)
	dir, err := SafeJoin(cfg.ExecPath(proj), sub)
	if err != nil {
		return "", fmt.Errorf("exchange_subdir %q invalid: %w", sub, err)
	}
	return dir, nil
}

// ResultBaseDir returns the base directory under which per-job result dirs are
// created. It branches on storage.root (§6.1, §9-P2):
//   - root unset (default): <host_path>/<exchange_subdir>/<result_subdir>
//   - root set (global store): <storage.root>/<project_key>
func ResultBaseDir(cfg *config.Config, projKey string, proj config.ProjectConfig) (string, error) {
	if cfg.Storage.Root != "" {
		rootAbs, err := filepath.Abs(cfg.Storage.Root)
		if err != nil {
			return "", fmt.Errorf("resolve storage.root: %w", err)
		}
		return filepath.Join(rootAbs, projKey), nil
	}

	exchange, err := ExchangeDir(cfg, proj)
	if err != nil {
		return "", err
	}
	resultSub := cfg.ResolvedResultSubdir(proj)
	// result_subdir must also stay inside the project (relative to exchange).
	if err := assertRelative(resultSub); err != nil {
		return "", fmt.Errorf("result_subdir %q invalid: %w", resultSub, err)
	}
	normalized := strings.ReplaceAll(resultSub, "\\", "/")
	joined := filepath.Join(exchange, filepath.Clean(normalized))
	rel, err := filepath.Rel(exchange, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("result_subdir %q escapes exchange dir", resultSub)
	}
	return joined, nil
}

// JobResultDir returns the result directory for a specific job id.
func JobResultDir(cfg *config.Config, projKey string, proj config.ProjectConfig, jobID string) (string, error) {
	base, err := ResultBaseDir(cfg, projKey, proj)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, jobID), nil
}
