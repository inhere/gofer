package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

type ptySessionView struct {
	PtySessionID string `json:"pty_session_id"`
	JobID        string `json:"job_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	State        string `json:"state"`
	Cols         int    `json:"cols"`
	Rows         int    `json:"rows"`
	BytesIn      int64  `json:"bytes_in"`
	BytesOut     int64  `json:"bytes_out"`
	Encrypted    bool   `json:"encrypted"`
	StartedAt    int64  `json:"started_at"`
	EndedAt      int64  `json:"ended_at"`
	HasRecording bool   `json:"has_recording"`
}

func (s *Server) handlePtySessions(c *rux.Context) {
	id := c.Param("id")
	caller := callerFromCtx(c)

	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.callerMayAttach(caller, res) {
		writeError(c, http.StatusForbidden, "pty sessions not permitted for this caller",
			"caller lacks permission to read this job's pty sessions")
		return
	}
	if s.ptySessions == nil {
		c.JSON(http.StatusOK, map[string]any{"sessions": []ptySessionView{}})
		return
	}
	rows, err := s.ptySessions.ListPtySessionsByJob(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "pty sessions lookup failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"sessions": s.ptySessionViews(rows, false)})
}

func (s *Server) handleRecentPtySessions(c *rux.Context) {
	caller := callerFromCtx(c)
	limit := parsePtySessionsLimit(c.Query("limit"))
	if s.ptySessions == nil {
		c.JSON(http.StatusOK, map[string]any{"sessions": []ptySessionView{}})
		return
	}
	rows, err := s.ptySessions.ListRecentPtySessions(limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "pty sessions lookup failed", err.Error())
		return
	}

	if s.cfg == nil || !s.cfg.CallerCanAdmin(caller) {
		filtered := rows[:0]
		for _, r := range rows {
			if caller != "" && r.Owner == caller {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	c.JSON(http.StatusOK, map[string]any{"sessions": s.ptySessionViews(rows, true)})
}

func parsePtySessionsLimit(raw string) int {
	if raw == "" {
		return 50
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}

func (s *Server) ptySessionViews(rows []jobstore.PtySessionRecord, includeJobID bool) []ptySessionView {
	out := make([]ptySessionView, 0, len(rows))
	for _, r := range rows {
		v := ptySessionView{
			PtySessionID: r.PtySessionID,
			SessionID:    s.sessionIDForJob(r.JobID),
			State:        r.State,
			Cols:         r.Cols,
			Rows:         r.Rows,
			BytesIn:      r.BytesIn,
			BytesOut:     r.BytesOut,
			Encrypted:    r.Encrypted == 1,
			StartedAt:    r.StartedAt,
			EndedAt:      r.EndedAt,
			HasRecording: r.RecordingURI != "",
		}
		if includeJobID {
			v.JobID = r.JobID
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) sessionIDForJob(jobID string) string {
	if s == nil || s.jobs == nil || jobID == "" {
		return ""
	}
	res, ok := s.jobs.Get(jobID)
	if !ok {
		return ""
	}
	return res.SessionID
}
