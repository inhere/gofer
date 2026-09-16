package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
)

// WT-01 受管 worktree 的管理端点。两者都只做参数绑定 + job 包 sentinel 到 HTTP 状态的
// 映射（G021：入口层不放业务/编排），git 动作全在 job.Service 内。
//
// 语义：GET 回该 job worktree 的**实时**状态（路径/分支/head/领先提交数/是否有未提交
// 改动/分支是否已合并）；DELETE 执行 `git worktree remove`，worktree 仍有未提交改动且
// 未带 ?force=1 时拒绝（409），?delete_branch=1 连带删除分支（交付物，故默认保留）。
// 注意 worktree 在**执行机**上：runner=worker 的 job，其路径在 worker 机器上，本服务
// 探测不到 → Exists=false（DB 记录的路径/分支仍原样返回）。

// handleGetJobWorktree serves GET /v1/jobs/{id}/worktree.
func (s *Server) handleGetJobWorktree(c *rux.Context) {
	st, err := s.jobs.WorktreeStatus(c.Param("id"))
	if err != nil {
		writeWorktreeError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// handleDeleteJobWorktree serves DELETE /v1/jobs/{id}/worktree and returns the
// final state (Exists=false) so the caller sees exactly what was removed.
func (s *Server) handleDeleteJobWorktree(c *rux.Context) {
	st, err := s.jobs.RemoveWorktree(c.Param("id"), queryBool(c, "force"), queryBool(c, "delete_branch"))
	if err != nil {
		writeWorktreeError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// queryBool reads a boolean query flag accepting the same spellings as ?wait=1
// (1/true), so a caller can use either.
func queryBool(c *rux.Context, name string) bool {
	v := c.Query(name)
	return v == "1" || v == "true"
}

// writeWorktreeError maps the job-package worktree sentinels to statuses:
//   - unknown job / job without a managed worktree → 404 (nothing to manage there);
//   - a worktree that still holds uncommitted changes, or whose directory is already
//     gone → 409 (a conflict with the on-disk state that the caller resolves either by
//     deciding to discard the work, or by cleaning up on the owning machine);
//   - anything else (git failed, …) → 500 with the original detail.
func writeWorktreeError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, job.ErrJobNotFound), errors.Is(err, job.ErrNoManagedWorktree):
		writeError(c, http.StatusNotFound, err.Error(), "")
	case errors.Is(err, job.ErrWorktreeDirty), errors.Is(err, job.ErrWorktreeGone):
		writeError(c, http.StatusConflict, err.Error(), "")
	default:
		writeError(c, http.StatusInternalServerError, "worktree operation failed", err.Error())
	}
}
