package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/trigger"
)

// trigger_handler.go is the HTTP entry of the inbound trigger module (design
// 2026-10-28-trigger-design.md 3.3). It only binds and maps: reading the bounded body and
// the headers the trigger cares about, handing them to trigger.Engine, and turning the
// engine's answer into a status code. Signature check, replay window, dedup, rate limit,
// owner admission and the dispatch are all inside internal/trigger.

// triggerInvokeResponse is the 202 / 200 answer. It carries ids only: never the payload,
// the signature, a secret or the rendered prompt.
type triggerInvokeResponse struct {
	Trigger   string `json:"trigger"`
	RequestID string `json:"request_id"`
	JobID     string `json:"job_id,omitempty"`
	Duplicate bool   `json:"duplicate"`
	// Status is the receipt status of the first delivery, set for a duplicate only.
	Status string `json:"status,omitempty"`
}

// handleTriggerInvoke is POST /v1/triggers/{name}/invoke, the EXTERNAL entry. It is
// registered outside the /v1 auth group: the sender has no gofer bearer, the trigger's HMAC
// secret is the whole credential, and the owner caller (not the sender) bounds the job.
//
// An unknown name, a disabled trigger and a server without a trigger engine all answer the
// same 404, before the body is read, so the response does not tell whether a trigger exists.
func (s *Server) handleTriggerInvoke(c *rux.Context) {
	name := c.Param("name")
	ep, ok := s.triggers.Endpoint(name)
	if !ok {
		s.writeTriggerNotFound(c)
		return
	}
	headers, err := ep.CollectHeaders(c.Req.Header.Get)
	if err != nil {
		s.writeTriggerInvokeError(c, name, "", err)
		return
	}
	// One byte more than the limit tells "exactly at the limit" from "over it".
	body, err := io.ReadAll(io.LimitReader(c.Req.Body, int64(ep.MaxBodyBytes)+1))
	if err != nil {
		s.writeErrorKey(c, http.StatusBadRequest, "trigger request unreadable", "trigger_body_unreadable")
		return
	}
	res, err := s.triggers.Invoke(c.Req.Context(), trigger.TriggerRequest{
		TriggerName: name, RawBody: body, Headers: headers, Source: "http", RemoteAddr: remoteHost(c.Req.RemoteAddr),
	})
	if err != nil {
		s.writeTriggerInvokeError(c, name, res.RequestID, err)
		return
	}
	out := triggerInvokeResponse{Trigger: name, RequestID: res.RequestID, JobID: res.JobID, Duplicate: res.Duplicate, Status: res.Status}
	status := http.StatusAccepted
	if res.Duplicate {
		// A known result: 200 lets the sender stop retrying.
		status = http.StatusOK
	}
	c.JSON(status, out)
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func (s *Server) writeTriggerNotFound(c *rux.Context) {
	s.writeErrorKey(c, http.StatusNotFound, "unknown trigger", "trigger_not_found")
}

// writeTriggerInvokeError maps the engine's refusal to a status and a stable code. The
// detail is the catalog text of the code; the engine's own error text (which may name a
// variable or a template) is never echoed to the sender.
func (s *Server) writeTriggerInvokeError(c *rux.Context, name, requestID string, err error) {
	var rl *trigger.RateLimitedError
	switch {
	case errors.Is(err, trigger.ErrUnknownTrigger):
		s.writeTriggerNotFound(c)
	case errors.Is(err, trigger.ErrReplay):
		s.writeErrorKey(c, http.StatusUnauthorized, "trigger timestamp rejected", "trigger_replay_window")
	case errors.Is(err, trigger.ErrUnauthenticated):
		s.writeErrorKey(c, http.StatusUnauthorized, "trigger authentication failed", "trigger_unauthenticated")
	case errors.As(err, &rl):
		secs := int((rl.RetryAfter + 999_999_999) / 1_000_000_000)
		if secs < 1 {
			secs = 1
		}
		c.SetHeader("Retry-After", strconv.Itoa(secs))
		s.writeErrorKey(c, http.StatusTooManyRequests, "trigger rate limited", "trigger_rate_limited", "seconds", secs)
	case errors.Is(err, trigger.ErrBodyTooLarge):
		s.writeErrorKey(c, http.StatusBadRequest, "trigger body too large", "trigger_body_too_large")
	case errors.Is(err, trigger.ErrHeaderTooLarge):
		s.writeErrorKey(c, http.StatusBadRequest, "trigger header too large", "trigger_header_too_large")
	case errors.Is(err, trigger.ErrBodyNotJSON):
		s.writeErrorKey(c, http.StatusBadRequest, "trigger body is not a JSON object", "trigger_body_not_json")
	case errors.Is(err, trigger.ErrInvalidInput):
		s.writeErrorKey(c, http.StatusBadRequest, "trigger input invalid", "trigger_input_invalid")
	case errors.Is(err, trigger.ErrEventIDTooLong):
		s.writeErrorKey(c, http.StatusBadRequest, "trigger event id invalid", "trigger_event_id_invalid")
	case errors.Is(err, jobstore.ErrTriggerReceiptConflict):
		s.writeErrorKey(c, http.StatusConflict, "trigger event id reused", "trigger_event_conflict")
	case errors.Is(err, trigger.ErrEventInFlight):
		c.SetHeader("Retry-After", "1")
		s.writeErrorKey(c, http.StatusConflict, "trigger event in flight", "trigger_event_in_flight")
	case errors.Is(err, trigger.ErrOwnerDenied):
		s.writeErrorKey(c, http.StatusForbidden, "trigger owner not permitted", "trigger_owner_denied")
	case errors.Is(err, job.ErrCeilingDenied):
		s.writeErrorKey(c, http.StatusForbidden, "trigger owner ceiling", "trigger_ceiling_denied")
	case errors.Is(err, job.ErrInvalidRequest), errors.Is(err, job.ErrUnknownProject):
		s.writeErrorKey(c, http.StatusBadRequest, "trigger request invalid", "trigger_request_invalid")
	default:
		// The reason stays in the log (ids only) and the audit trail; the sender gets none.
		slog.Warn("trigger.invoke_failed", "component", "server", "trigger", name, "request_id", requestID)
		s.writeErrorKey(c, http.StatusServiceUnavailable, "trigger unavailable", "trigger_unavailable")
	}
}

// triggerReadAllowed refuses a job credential: the redacted trigger views are an operator
// read (trigger.read), and the SEC-01 table lets a job read the whole GET surface, so the
// refusal is stated here.
func (s *Server) triggerReadAllowed(c *rux.Context) bool {
	if callerKindFromCtx(c) == callerKindJob {
		s.writeErrorKey(c, http.StatusForbidden, "trigger read refused", "trigger_read_job_credential")
		return false
	}
	return true
}

// handleTriggerList is GET /v1/triggers.
func (s *Server) handleTriggerList(c *rux.Context) {
	if !s.triggerReadAllowed(c) {
		return
	}
	views := s.triggers.List()
	if views == nil {
		views = []trigger.View{}
	}
	c.JSON(http.StatusOK, map[string]any{"triggers": views})
}

// handleTriggerGet is GET /v1/triggers/{name}.
func (s *Server) handleTriggerGet(c *rux.Context) {
	if !s.triggerReadAllowed(c) {
		return
	}
	v, ok := s.triggers.Show(c.Param("name"))
	if !ok {
		s.writeTriggerNotFound(c)
		return
	}
	c.JSON(http.StatusOK, v)
}

// handleTriggerDeliveries is GET /v1/triggers/{name}/deliveries?limit=N.
func (s *Server) handleTriggerDeliveries(c *rux.Context) {
	if !s.triggerReadAllowed(c) {
		return
	}
	name := c.Param("name")
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, known, err := s.triggers.Deliveries(name, limit)
	if !known {
		s.writeTriggerNotFound(c)
		return
	}
	if err != nil {
		slog.Warn("trigger.deliveries_failed", "component", "server", "trigger", name)
		s.writeErrorKey(c, http.StatusServiceUnavailable, "trigger unavailable", "trigger_unavailable")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"trigger": name, "deliveries": rows})
}
