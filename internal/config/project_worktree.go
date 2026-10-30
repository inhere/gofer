package config

import (
	"fmt"
	"path"
	"strings"
)

// Worktree 模板（WT-01 扩展，2026-10-21 design「一、worktree 的位置、分支名与依赖链接」）。
//
// 项目的 `worktree:` 段只描述「本项目的 --worktree job 落在哪、分支叫什么、从主 checkout
// 链接哪些目录过来」，默认值为仓库顶层的 `.worktrees/{name}`（{name} 未给时为 job id）。
// 模板展开在提交侧完成（见 job.worktreeOptionsFor），执行机拿到的是已解析好的路径与分支。
const (
	// DefaultWorktreeDir 是项目没配 worktree.dir 时的落点模板（相对仓库顶层）。
	DefaultWorktreeDir = ".worktrees/{name}"
	// DefaultWorktreeBranch 是项目没配 worktree.branch 时的分支模板，完整名 gofer/<job-id>。
	DefaultWorktreeBranch = "gofer/{job_id}"
	// worktreeNamePlaceholder / worktreeJobIDPlaceholder 是模板里仅有的两个占位符。
	// {name} 来自 `job run --worktree-name`（未给时等于 job id）。
	worktreeNamePlaceholder  = "{name}"
	worktreeJobIDPlaceholder = "{job_id}"
)

// ProjectWorktree 是项目配置的 `worktree:` 段（WT-01 扩展）。
type ProjectWorktree struct {
	// Dir is the worktree checkout dir, relative to the repository toplevel, with
	// {name} / {job_id} placeholders. Empty = DefaultWorktreeDir.
	Dir string `yaml:"dir,omitempty"`
	// Branch is the branch template ({name} / {job_id}); `job run --branch` overrides
	// it for one job. Empty = DefaultWorktreeBranch.
	Branch string `yaml:"branch,omitempty"`
	// Links lists project-relative directories to link from the main checkout into
	// the fresh worktree (Windows directory junction, elsewhere a symlink), so a job
	// in the worktree finds the dependencies it does not commit (e.g. a vendored
	// node_modules). A missing source is skipped, not fatal.
	Links []string `yaml:"links,omitempty"`
}

// RenderDir 展开 worktree.dir 模板。name 为空时取 jobID（`--worktree-name` 未给）。
func (p *ProjectConfig) RenderWorktreeDir(name, jobID string) string {
	w := p.Worktree
	if w == nil {
		return renderWorktreeTemplate(DefaultWorktreeDir, DefaultWorktreeDir, name, jobID)
	}
	return renderWorktreeTemplate(w.Dir, DefaultWorktreeDir, name, jobID)
}

// RenderWorktreeBranch 展开 worktree.branch 模板，口径同 RenderWorktreeDir。
func (p *ProjectConfig) RenderWorktreeBranch(name, jobID string) string {
	w := p.Worktree
	if w == nil {
		return renderWorktreeTemplate(DefaultWorktreeBranch, DefaultWorktreeBranch, name, jobID)
	}
	return renderWorktreeTemplate(w.Branch, DefaultWorktreeBranch, name, jobID)
}

// WorktreeLinks 返回本项目要在 worktree 里创建的依赖链接（项目内相对路径）；没配即为空。
func (p *ProjectConfig) WorktreeLinks() []string {
	if p.Worktree == nil {
		return nil
	}
	return p.Worktree.Links
}

// renderWorktreeTemplate 展开 {name} / {job_id}；空模板回落到该字段自己的默认值。
func renderWorktreeTemplate(tmpl, def, name, jobID string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = def
	}
	if name == "" {
		name = jobID
	}
	return strings.NewReplacer(
		worktreeNamePlaceholder, name,
		worktreeJobIDPlaceholder, jobID,
	).Replace(tmpl)
}

// WorktreeNameValid 报告一个 `--worktree-name` 是否能安全地拼进路径与分支名：
// 只允许 [A-Za-z0-9._-]，且不能是 "." / ".."。空名合法（表示沿用 job id）。
func WorktreeNameValid(name string) bool {
	if name == "" {
		return true
	}
	if name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// validateWorktree 校验项目的 worktree 段（config validate / 加载时）：模板只能含两个
// 已知占位符、展开后仍是仓库内相对路径，links 是项目内相对路径且不含 `..`。
// 模板用样本值展开，因此校验不需要 job。
func (p *ProjectConfig) validateWorktree(projectKey string) error {
	w := p.Worktree
	if w == nil {
		return nil
	}
	if err := checkWorktreeTemplate("worktree.dir", w.Dir, DefaultWorktreeDir); err != nil {
		return fmt.Errorf("project %q: %w", projectKey, err)
	}
	if err := checkWorktreeRelative("worktree.dir", renderWorktreeTemplate(w.Dir, DefaultWorktreeDir, "name", "job1")); err != nil {
		return fmt.Errorf("project %q: %w", projectKey, err)
	}
	if err := checkWorktreeTemplate("worktree.branch", w.Branch, DefaultWorktreeBranch); err != nil {
		return fmt.Errorf("project %q: %w", projectKey, err)
	}
	if branch := renderWorktreeTemplate(w.Branch, DefaultWorktreeBranch, "name", "job1"); strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return fmt.Errorf("project %q: worktree.branch %q must not start with '-' or contain '..'", projectKey, branch)
	}
	for i, link := range w.Links {
		if strings.Contains(link, worktreeNamePlaceholder) || strings.Contains(link, worktreeJobIDPlaceholder) {
			return fmt.Errorf("project %q: worktree.links[%d] %q must not contain template placeholders", projectKey, i, link)
		}
		if err := checkWorktreeRelative(fmt.Sprintf("worktree.links[%d]", i), link); err != nil {
			return fmt.Errorf("project %q: %w", projectKey, err)
		}
	}
	return nil
}

// checkWorktreeTemplate 拒绝无法展开的模板：出现未知的 `{...}` 占位符会被原样拼进路径，
// 在提交时才发现就太晚了（--worktree 的 job 已经排队）。
func checkWorktreeTemplate(field, tmpl, def string) error {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = def
	}
	if strings.ContainsAny(tmpl, "\r\n\x00") {
		return fmt.Errorf("%s: template must be a single line", field)
	}
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '{' {
			continue
		}
		rest := tmpl[i:]
		if strings.HasPrefix(rest, worktreeNamePlaceholder) {
			i += len(worktreeNamePlaceholder) - 1
			continue
		}
		if strings.HasPrefix(rest, worktreeJobIDPlaceholder) {
			i += len(worktreeJobIDPlaceholder) - 1
			continue
		}
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return fmt.Errorf("%s: unterminated placeholder in %q", field, tmpl)
		}
		return fmt.Errorf("%s: unknown placeholder %q in %q (want %s or %s)", field, rest[:end+1], tmpl, worktreeNamePlaceholder, worktreeJobIDPlaceholder)
	}
	return nil
}

// checkWorktreeRelative 要求路径是仓库内相对路径：非绝对、不含 `..`、不是当前目录。
func checkWorktreeRelative(field, p string) error {
	if p == "" {
		return fmt.Errorf("%s: path must not be empty", field)
	}
	if strings.ContainsAny(p, "\r\n\x00") {
		return fmt.Errorf("%s: path must be a single line", field)
	}
	slashed := strings.ReplaceAll(p, "\\", "/")
	cleaned := path.Clean(slashed)
	if path.IsAbs(cleaned) || isWindowsAbs(slashed) {
		return fmt.Errorf("%s: %q must be relative to the project root", field, p)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%s: %q must not escape the project root", field, p)
	}
	if cleaned == "." {
		return fmt.Errorf("%s: %q must name a path inside the project root", field, p)
	}
	return nil
}

// isWindowsAbs reports a Windows drive-letter or UNC path; config validation runs on
// every platform, so a `C:\...` template must be rejected on a Linux server too.
func isWindowsAbs(p string) bool {
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}
