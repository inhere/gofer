package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

// bindTrackerProjectKey writes the gofer project key that owns root into the
// tracker config (best effort) and returns the lines that tell the user which
// project matched and why, or how to fill project_key when nothing matched.
func bindTrackerProjectKey(s *tracker.Store, root string) []string {
	return bindTrackerProjectKeyWith(s, root, fetchServerProjects)
}

// fetchServerProjects asks the configured server for its project list (with
// paths); a short deadline keeps init/migrate snappy when the server is down.
func fetchServerProjects() ([]client.ProjectMeta, error) {
	cli, err := trackerClient()
	if err != nil {
		return nil, err
	}
	return client.NewWithTimeout(cli.BaseURL(), cli.Token(), 3*time.Second).ListProjects()
}

// bindTrackerProjectKeyWith matches root to a project: first the local config,
// then (for client nodes whose config carries no projects) the server's project
// list by host/container path. fetch is injectable for tests.
func bindTrackerProjectKeyWith(s *tracker.Store, root string, fetch func() ([]client.ProjectMeta, error)) []string {
	appCfg, _, err := config.Load(config.InputCfgFile)
	if err == nil {
		if key, _, _ := appCfg.ProjectMatchForPath(root); key != "" {
			_ = s.SetProjectKey(key)
			return trackerProjectKeyNotes(appCfg, root, key)
		}
	}
	if projects, ferr := fetch(); ferr == nil {
		if key, projRoot, ok := client.MatchProjectPath(projects, root); ok {
			_ = s.SetProjectKey(key)
			return []string{fmt.Sprintf("project_key: %s（匹配依据：server 项目路径最长前缀 %s）", key, projRoot)}
		}
	}
	if err != nil {
		return trackerProjectKeyNotes(nil, root, "")
	}
	return trackerProjectKeyNotes(appCfg, root, "")
}

// trackerProjectKeyNotes explains the project match for root. cfg == nil means
// no gofer config could be loaded. key is the matched project ("" = none).
func trackerProjectKeyNotes(cfg *config.Config, root, key string) []string {
	if cfg == nil {
		return []string{"project_key: 未写入（没有可用的 gofer 配置）；可在 .gofer/tracker/config.yaml 填写 project_key"}
	}
	_, projRoot, ok := cfg.ProjectMatchForPath(root)
	if !ok || key == "" {
		return []string{"project_key: 未匹配到 gofer 项目（目录不在任何已登记项目路径下，或与多个项目并列）；" +
			"可 `gofer project add` 登记该目录，或手工在 .gofer/tracker/config.yaml 填写 project_key"}
	}
	notes := []string{fmt.Sprintf("project_key: %s（匹配依据：最长路径前缀 %s）", key, projRoot)}
	if outer := nestedOuterRepo(root, projRoot); outer != "" {
		notes = append(notes, fmt.Sprintf(
			"提示: 这是嵌套的独立仓库（上层仓库 %s 也在项目 %s 目录下），已归属项目 %s；"+
				"如需独立，可注册单独项目并改 .gofer/tracker/config.yaml 的 project_key", outer, key, key))
	}
	return notes
}

// nestedOuterRepo returns the nearest enclosing git repository root that sits
// above root but still inside projRoot (inclusive), when root itself is a git
// repository; "" otherwise.
func nestedOuterRepo(root, projRoot string) string {
	root, projRoot = filepath.Clean(root), filepath.Clean(projRoot)
	if !hasGit(root) || root == projRoot {
		return ""
	}
	for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
		if hasGit(dir) {
			return dir
		}
		if dir == projRoot || dir == filepath.Dir(dir) {
			return ""
		}
	}
}

func hasGit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git")) // dir, or a file for worktrees/submodules
	return err == nil
}

func printNotes(c *gcli.Command, notes []string) {
	if len(notes) > 0 {
		c.Println(strings.Join(notes, "\n"))
	}
}

// serverProjectKey checks key against the server's project list and, when the
// server has no such project (a repository may carry its own label, e.g. gofer's
// project_key: gofer), falls back to the longest host/container path prefix of
// root. Any failure keeps key: prime is best effort.
func serverProjectKey(cli interface {
	ListProjects() ([]client.ProjectMeta, error)
}, key, root string) string {
	projects, err := cli.ListProjects()
	if err != nil {
		return key
	}
	for _, p := range projects {
		if p.Key == key {
			return key
		}
	}
	if matched, _, ok := client.MatchProjectPath(projects, root); ok {
		return matched
	}
	return key
}
