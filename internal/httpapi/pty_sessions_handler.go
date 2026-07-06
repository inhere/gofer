package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

type ptySessionView struct {
	PtySessionID string `json:"pty_session_id"`
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
	c.JSON(http.StatusOK, map[string]any{"sessions": ptySessionViews(rows)})
}

func ptySessionViews(rows []jobstore.PtySessionRecord) []ptySessionView {
	out := make([]ptySessionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ptySessionView{
			PtySessionID: r.PtySessionID,
			State:        r.State,
			Cols:         r.Cols,
			Rows:         r.Rows,
			BytesIn:      r.BytesIn,
			BytesOut:     r.BytesOut,
			Encrypted:    r.Encrypted == 1,
			StartedAt:    r.StartedAt,
			EndedAt:      r.EndedAt,
			HasRecording: r.RecordingURI != "",
		})
	}
	return out
}
