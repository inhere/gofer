package httpapi

import (
	"errors"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
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

// handleGetJobWorktree serves GET /v1/jobs/{id}/worktree. InHandler(job.read.others, deny)
// on the job's owner (plan T1.5a).
func (s *Server) handleGetJobWorktree(c *rux.Context) {
	if res, ok := s.jobForAccess(c.Param("id")); ok && !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	st, err := s.jobs.WorktreeStatus(c.Param("id"))
	if err != nil {
		writeWorktreeError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

// handleDeleteJobWorktree serves DELETE /v1/jobs/{id}/worktree and returns the
// final state (Exists=false) so the caller sees exactly what was removed.
// InHandler(job.manage.others, deny) on the job's owner (plan T1.5a).
func (s *Server) handleDeleteJobWorktree(c *rux.Context) {
	if res, ok := s.jobForAccess(c.Param("id")); ok && !s.mayManageJob(c, res, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return
	}
	st, err := s.jobs.RemoveWorktree(c.Param("id"), queryBool(c, "force"), queryBool(c, "delete_branch"))
	if err != nil {
		writeWorktreeError(c, err)
		return
	}
	c.JSON(http.StatusOK, st)
}

type mergeWorktreeRequest struct {
	Squash        bool   `json:"squash"`
	CleanupOthers bool   `json:"cleanup_others"`
	TodoDone      bool   `json:"todo_done"`
	Note          string `json:"note,omitempty"`
	Comment       string `json:"comment,omitempty"`
	Cleanup       bool   `json:"cleanup"`
}

// handleMergeJobWorktree merges a local managed worktree branch into the main
// checkout. The job package performs all git checks and aborts conflicts.
// InHandler(job.manage.others, deny) on the job's owner (plan T1.5a).
func (s *Server) handleMergeJobWorktree(c *rux.Context) {
	var req mergeWorktreeRequest
	if err := c.BindJSON(&req); err != nil && err.Error() != "EOF" {
		writeError(c, http.StatusBadRequest, "invalid merge request", err.Error())
		return
	}
	if res, ok := s.jobForAccess(c.Param("id")); ok && !s.mayManageJob(c, res, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return
	}
	opts := job.MergeOptions{Squash: req.Squash, CleanupOthers: req.CleanupOthers,
		TodoDone: req.TodoDone, Note: req.Note, Comment: req.Comment, Cleanup: req.Cleanup}
	if opts.Comment != "" {
		// The comment speaks as the CREDENTIAL, exactly like POST /v1/jobs/{id}/comments.
		opts.CommentAuthor, opts.CommentAuthorKind = s.commentAuthor(c)
	}
	st, err := s.jobs.MergeWorktree(c.Param("id"), opts)
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
	case errors.Is(err, job.ErrWorktreeMergeConflict), errors.Is(err, job.ErrWorktreeMergeUnsupported), errors.Is(err, job.ErrWorktreeMainNotReady),
		errors.Is(err, job.ErrWorktreeIndexLocked):
		writeError(c, http.StatusConflict, err.Error(), "")
	default:
		writeError(c, http.StatusInternalServerError, "worktree operation failed", err.Error())
	}
}

// deliverJobRequest is POST /v1/jobs/{id}/deliver (GIT-02).
type deliverJobRequest struct {
	Mode   string `json:"mode,omitempty"`
	Draft  *bool  `json:"draft,omitempty"`
	Branch string `json:"branch,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// handleDeliverJob hands a done worktree job's branch over (merge / push / pull
// request) now, overriding the mode / draft / branch when given. A person's action:
// it may push — worker and job credentials are refused. The outcome (also recorded on
// the job) is returned; a delivery that ran but failed is a 200 with state failed.
func (s *Server) handleDeliverJob(c *rux.Context) {
	if callerKindFromCtx(c) == callerKindWorker {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "worker tokens cannot deliver jobs")
		return
	}
	if _, isJob := jobCallerFromCtx(c); isJob {
		writeError(c, http.StatusForbidden, "not permitted for this caller", "a job cannot deliver itself; delivery follows the project config or a person")
		return
	}
	// InHandler(job.manage.others, deny) on the job's owner (plan T1.5a).
	if res, ok := s.jobForAccess(c.Param("id")); ok && !s.mayManageJob(c, res, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return
	}
	var req deliverJobRequest
	if err := c.BindJSON(&req); err != nil && err.Error() != "EOF" {
		writeError(c, http.StatusBadRequest, "invalid deliver request", err.Error())
		return
	}
	opts := job.DeliverOptions{Mode: req.Mode, Draft: req.Draft, Branch: req.Branch, DryRun: req.DryRun}
	var res any
	var err error
	if req.DryRun {
		res, err = s.jobs.PreviewDelivery(c.Param("id"), opts)
	} else {
		res, err = s.jobs.Deliver(c.Param("id"), opts)
	}
	switch {
	case err == nil:
		c.JSON(http.StatusOK, res)
	case errors.Is(err, job.ErrUnknownJob):
		writeError(c, http.StatusNotFound, err.Error(), "")
	case errors.Is(err, job.ErrDeliveryNotApplicable):
		writeError(c, http.StatusConflict, err.Error(), "")
	case errors.Is(err, job.ErrInvalidRequest):
		writeError(c, http.StatusBadRequest, err.Error(), "")
	default:
		writeError(c, http.StatusInternalServerError, "delivery failed", err.Error())
	}
}
