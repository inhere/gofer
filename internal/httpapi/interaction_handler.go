package httpapi

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

// createInteractionReq is the POST body for raising a running-job interaction
// (plan §P9). Fields are snake_case to match the rest of the control plane.
type createInteractionReq struct {
	Type    string                  `json:"type"`
	Prompt  string                  `json:"prompt"`
	Options []job.InteractionOption `json:"options,omitempty"`
}

// answerInteractionReq is the POST body for answering an interaction. An empty
// answer is allowed (e.g. an explicit empty confirmation), so this never fails
// validation on its own.
type answerInteractionReq struct {
	Answer string `json:"answer"`
}

// handleCreateInteraction raises a new interaction on a live job and returns the
// created job.Interaction (already snake_case). Validation/state errors map to a
// stable status via interactionStatus.
//
// 注入 interaction 是「他人 job」的动作（设计 §1：job.manage.others 覆盖「注入 interaction」），
// so the rule is mayManageJob: the caller's own job needs nothing. An unknown job keeps the
// service's own 404 (the permission rule never invents a different answer for a job that is
// not there).
func (s *Server) handleCreateInteraction(c *rux.Context) {
	id := c.Param("id")
	var req createInteractionReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if res, ok := s.jobForAccess(id); ok && !s.mayManageJob(c, res, false) {
		s.denyJobAction(c, authz.PermJobManageOthers)
		return
	}

	it, err := s.jobs.CreateInteraction(id, job.InteractionInput{
		Type:    req.Type,
		Prompt:  req.Prompt,
		Options: req.Options,
	})
	if err != nil {
		writeError(c, interactionStatus(err), "create interaction failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, it)
}

// handleListInteractions returns the job's interactions. An unknown job id is a
// 404 (consistent with handleGetJob). The list is always a non-nil array, so an
// empty result serialises as {"interactions":[]}.
//
// 读他人 job 的 interaction 需要 job.read.others（设计 §1，声明 InHandler(permDeny, job.read.others)）。
//
// It reads via GetPersistedInteractions (not the in-memory-only GetInteractions):
// after SP3 a finished job is evicted from the in-memory map, so the live state
// is gone — the interactions.jsonl fallback (folded by GetPersistedInteractions)
// is what surfaces a terminated job's interaction history. The result base is
// derived from the job's ResultDir (== <base>/<job_id>), which Get serves from
// the DB even for evicted jobs.
func (s *Server) handleListInteractions(c *rux.Context) {
	id := c.Param("id")
	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.mayReadJob(c, res) {
		s.denyJobAction(c, authz.PermJobReadOthers)
		return
	}
	base := filepath.Dir(res.ResultDir)
	list, _ := s.jobs.GetPersistedInteractions(base, id)
	if list == nil {
		list = []job.Interaction{}
	}
	c.JSON(http.StatusOK, map[string]any{"interactions": list})
}

// handleListPendingInteractions returns the pending interactions across all ACTIVE
// jobs (E25 监督发现). MVP supports only ?status=pending (any other value is a 400);
// an absent status defaults to pending. The result is always a non-nil array, each
// element carrying its job_id so the caller can route an answer.
//
// 跨 job 的列表是 job.read.others 的**过滤型**用法（设计 §1：没有权限时只返回自己的数据，
// 不报 403）：without the permission the answer narrows to the interactions of the caller's
// OWN jobs — the same divide mayListAllJobs draws for the job and workflow lists. A worker
// token and a job credential keep the whole list, as they did before the migration (their
// own tables admitted the route; see mayListAllJobs).
func (s *Server) handleListPendingInteractions(c *rux.Context) {
	if st := c.Query("status"); st != "" && st != job.InteractionPending {
		writeError(c, http.StatusBadRequest, "unsupported status filter", "only status=pending is supported")
		return
	}
	list, err := s.jobs.ListPendingInteractions()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list pending interactions failed", err.Error())
		return
	}
	if !s.mayListAllJobs(c) {
		list = s.ownPendingInteractions(c, list)
	}
	list = scopeRowsByKind(s, c, "job", list, func(i job.Interaction) string { return i.JobID })
	if list == nil {
		list = []job.Interaction{}
	}
	c.JSON(http.StatusOK, map[string]any{"interactions": list})
}

// ownPendingInteractions narrows a pending-interaction list to the caller's own jobs — the
// filter half of `InHandler(job.read.others, filter)`. The owning job is looked up per row
// (the pending list is short and carries only live jobs); a row whose job cannot be resolved
// anymore is dropped, because without job.read.others the caller only sees what it owns.
func (s *Server) ownPendingInteractions(c *rux.Context, list []job.Interaction) []job.Interaction {
	caller := callerFromCtx(c)
	out := list[:0]
	for _, it := range list {
		res, ok := s.jobForAccess(it.JobID)
		if !ok || res.CallerID != caller {
			continue
		}
		out = append(out, it)
	}
	return out
}

// handleAnswerInteraction answers a pending interaction and returns the updated
// job.Interaction. Unknown job/interaction -> 404; terminal job -> 409; not
// pending -> 400.
//
// 答任何 job 的 interaction 都是「代答」（设计 §1：interaction.answer「回答 job 的工具审批与提问」），
// so the route declares Require(interaction.answer) and the gate refuses a caller without it —
// the replacement for the old hand-written answer-capability check.
func (s *Server) handleAnswerInteraction(c *rux.Context) {
	id := c.Param("id")
	iid := c.Param("interaction_id")
	var req answerInteractionReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	caller := callerFromCtx(c)

	it, err := s.jobs.AnswerInteractionByHuman(id, iid, req.Answer, caller)
	if err != nil {
		writeError(c, interactionStatus(err), "answer interaction failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, it)
}

// handlePuntInteraction marks a pending interaction needs_human — the通用 sup's "高危/拿不准,
// 留给人" decision (y5wt). It flips needs_human (excluding the interaction from the sup demand
// signal so the reconciler stops re-waking a sup for it) and leaves the interaction pending
// for a human. No body. MarkInteractionNeedsHuman is a targeted update / silent no-op for an
// unknown id, so this always returns 200 {"status":"ok"} unless the store write itself fails.
//
// punt 与答同权：设计 §1 把「punt」列在 interaction.answer 下，路由因此声明 Require(interaction.answer)。
func (s *Server) handlePuntInteraction(c *rux.Context) {
	id := c.Param("id")
	iid := c.Param("interaction_id")
	caller := callerFromCtx(c)
	if err := s.jobs.MarkInteractionNeedsHumanBy(id, iid, caller); err != nil {
		writeError(c, interactionStatus(err), "punt interaction failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"status": "ok"})
}

// interactionStatus maps a job-package interaction error to an HTTP status
// (mirrors submitStatus): unknown job/interaction -> 404; terminal job -> 409
// (Conflict); not-pending / invalid payload -> 400; anything else -> 400.
func interactionStatus(err error) int {
	switch {
	case errors.Is(err, job.ErrUnknownJob), errors.Is(err, job.ErrUnknownInteraction):
		return http.StatusNotFound
	case errors.Is(err, job.ErrJobTerminal):
		return http.StatusConflict
	default:
		// ErrInteractionState, ErrInvalidInteraction and any unclassified error.
		return http.StatusBadRequest
	}
}
