package httpapi

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/gookit/rux/v2"

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
func (s *Server) handleCreateInteraction(c *rux.Context) {
	id := c.Param("id")
	var req createInteractionReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
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
	if list == nil {
		list = []job.Interaction{}
	}
	c.JSON(http.StatusOK, map[string]any{"interactions": list})
}

// handleAnswerInteraction answers a pending interaction and returns the updated
// job.Interaction. Unknown job/interaction -> 404; terminal job -> 409; not
// pending -> 400.
func (s *Server) handleAnswerInteraction(c *rux.Context) {
	id := c.Param("id")
	iid := c.Param("interaction_id")
	var req answerInteractionReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	it, err := s.jobs.AnswerInteraction(id, iid, req.Answer)
	if err != nil {
		writeError(c, interactionStatus(err), "answer interaction failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, it)
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
