package project

import (
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// ResolveByCwd returns the project key whose host_path or container_path is the
// longest prefix of absCwd, plus cwd relative to that root (D7). It only uses the
// 注册锚 (host/container path), never the overlay. ok=false on zero match or a tie
// (两个项目同长前缀 → 让用户显式 -p, 避免误派).
func ResolveByCwd(cfg *config.Config, absCwd string) (key, relCwd string, ok bool) {
	bestLen := -1
	tie := false
	for k, p := range cfg.Projects {
		for _, root := range []string{p.ContainerPath, p.HostPath} {
			if root == "" {
				continue
			}
			abs, err := filepath.Abs(root)
			if err != nil {
				continue
			}
			if absCwd == abs || strings.HasPrefix(absCwd, abs+string(filepath.Separator)) {
				if len(abs) > bestLen {
					bestLen, key, tie = len(abs), k, false
					if rel, e := filepath.Rel(abs, absCwd); e == nil {
						relCwd = rel
					}
				} else if len(abs) == bestLen && k != key {
					tie = true
				}
			}
		}
	}
	if bestLen < 0 || tie {
		return "", "", false
	}
	if relCwd == "" {
		relCwd = "."
	}
	return key, relCwd, true
}
