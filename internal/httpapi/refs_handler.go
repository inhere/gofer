package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"
)

type refView struct {
	SrcKind     string `json:"source_kind"`
	SrcID       string `json:"source_id"`
	DstKind     string `json:"kind"`
	DstID       string `json:"id"`
	Field       string `json:"field"`
	CreatedAt   int64  `json:"created_at"`
	SourceTitle string `json:"source_title,omitempty"`
}

// handleListRefs returns sources that mention the requested object. Sources that
// are jobs are filtered by the same ownership/permission rule as job reads.
func (s *Server) handleListRefs(c *rux.Context) {
	kind, id := strings.TrimSpace(c.Query("kind")), strings.TrimSpace(c.Query("id"))
	if kind == "" || id == "" {
		writeError(c, http.StatusBadRequest, "invalid reference target", "kind and id are required")
		return
	}
	meta := s.jobs.Meta()
	if meta == nil {
		writeError(c, http.StatusServiceUnavailable, "refs unavailable", "job metadata store is not configured")
		return
	}
	rows, err := meta.ListRefs(kind, id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list refs failed", err.Error())
		return
	}
	out := make([]refView, 0, len(rows))
	for _, row := range rows {
		v := refView{SrcKind: row.SrcKind, SrcID: row.SrcID, DstKind: row.DstKind, DstID: row.DstID, Field: row.Field, CreatedAt: row.CreatedAt}
		switch row.SrcKind {
		case "job":
			job, ok := s.jobs.Get(row.SrcID)
			if !ok || !s.mayReadJob(c, job) {
				continue
			}
			v.SourceTitle = job.Title
		case "plan":
			if p, ok, _ := meta.GetPlan(row.SrcID); ok {
				v.SourceTitle = p.Title
			}
		case "todo":
			if todo, ok, _ := meta.GetTodo(row.SrcID); ok {
				v.SourceTitle = todo.Title
			}
		case "comment":
			if cm, ok, _ := meta.GetComment(row.SrcID); ok {
				v.SourceTitle = cm.Body
				if cm.Scope == "job" {
					job, exists := s.jobs.Get(cm.ScopeID)
					if !exists || !s.mayReadJob(c, job) {
						continue
					}
				}
			}
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, map[string]any{"refs": out})
}
