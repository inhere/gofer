package job

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// resolveReadDirs expands project-relative read directories, resolves symlinks and
// enforces project/allow-list containment. The returned paths are absolute paths on
// the executing machine and are persisted in request_json for auditability.
func resolveReadDirs(cfg *config.Config, proj config.ProjectConfig, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	root := filepath.Clean(cfg.ExecPath(proj))
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("read_dirs project root: %w", err)
	}
	allowed := []string{rootReal}
	for _, raw := range cfg.Server.ReadDirAllow {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("read_dir_allow path must be absolute: %q", raw)
		}
		real, e := filepath.EvalSymlinks(filepath.Clean(p))
		if e != nil {
			return nil, fmt.Errorf("read_dir_allow %q: %w", raw, e)
		}
		allowed = append(allowed, real)
	}
	out := make([]string, 0, len(requested))
	seen := make(map[string]bool)
	for _, raw := range requested {
		p := strings.TrimSpace(raw)
		if p == "" {
			return nil, fmt.Errorf("read_dir cannot be empty")
		}
		if readDirDotDot(filepath.ToSlash(p)) {
			return nil, fmt.Errorf("read_dir %q contains ..", raw)
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p, err = filepath.Abs(filepath.Clean(p))
		if err != nil {
			return nil, fmt.Errorf("read_dir %q: %w", raw, err)
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("read_dir %q does not exist: %w", raw, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("read_dir %q is not a directory", raw)
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, fmt.Errorf("read_dir %q: %w", raw, err)
		}
		ok := false
		for _, parent := range allowed {
			if readDirContained(parent, real) {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("read_dir %q is outside the project and server.read_dir_allow", raw)
		}
		key := util.RealPath(real)
		if !seen[key] {
			seen[key] = true
			out = append(out, real)
		}
	}
	return out, nil
}

func mergeReadDirs(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]string, 0, util.CapSum(len(a), len(b)))
	for _, p := range append(append([]string(nil), a...), b...) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		duplicate := false
		for _, old := range out {
			if sameDir(old, p) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, p)
		}
	}
	return out
}

func readDirArgs(ac config.AgentConfig, dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	if ac.Type != agent.TypeCLIAgent || len(ac.AddDirArgs) == 0 {
		return nil
	}
	out := make([]string, 0, util.CapMul(len(dirs), len(ac.AddDirArgs)))
	for _, dir := range dirs {
		out = append(out, agent.Render(ac.AddDirArgs, agent.Vars{Dir: dir})...)
	}
	return out
}

func mergeProjectReadDirs(cfg *config.Config, req *JobRequest, proj config.ProjectConfig) {
	if req == nil || cfg == nil {
		return
	}
	req.ReadDirs = mergeReadDirs(proj.ReadDirs, req.ReadDirs)
}

func readDirDotDot(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func readDirContained(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func resolveReadDirForRequest(cfg *config.Config, proj config.ProjectConfig, req *JobRequest) error {
	dirs, err := resolveReadDirs(cfg, proj, req.ReadDirs)
	if err != nil {
		return err
	}
	req.ReadDirs = dirs
	return nil
}

func validateReadDirAgent(req JobRequest, ac config.AgentConfig) error {
	if len(req.ReadDirs) == 0 {
		return nil
	}
	if ac.Type != agent.TypeCLIAgent || len(ac.AddDirArgs) == 0 {
		return fmt.Errorf("%w: agent %q does not support read_dirs (configure add_dir_args on a cli-agent)", ErrInvalidRequest, req.Agent)
	}
	return nil
}
