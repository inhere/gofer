package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/template"
)

// template_handler.go exposes the task-book template surface (SUP-01 P5, design
// §六) read-only: the submit path itself carries `template`/`vars` in the ordinary
// JobRequest, so there is nothing to write here. The console's submit form and
// `gofer template ls|show` read these two endpoints; the templates live on the
// SERVER's disk (the project's .gofer/templates first, then <config-dir>/templates),
// which is why they are read over HTTP rather than from a client's own filesystem.

// handleListProjectTemplates returns every template a project can be driven with.
// A broken template appears with its parse error rather than vanishing: that is the
// entry a reader is looking for when a submit failed.
func (s *Server) handleListProjectTemplates(c *rux.Context) {
	list, err := s.jobs.ListTemplates(c.Param("key"))
	if err != nil {
		writeError(c, templateStatus(err), "unknown project", err.Error())
		return
	}
	c.JSON(http.StatusOK, rux.M{"templates": list})
}

// handleGetProjectTemplate returns ONE template plus a render of it. The render is
// done HERE, not by the client: only the server can expand {{include: …}} from the
// template's directory and only the server knows the {{head}} of the checkout the job
// would run in, so a preview is exactly what a submit would send. Variables are the
// repeated `?var=k=v` query params (a preview is a GET; nothing is persisted).
func (s *Server) handleGetProjectTemplate(c *rux.Context) {
	preview, err := s.jobs.PreviewTemplate(c.Param("key"), c.Param("name"), queryVars(c))
	if err != nil {
		writeError(c, templateStatus(err), "template not found", err.Error())
		return
	}
	c.JSON(http.StatusOK, preview)
}

// queryVars reads the repeated `var=k=v` query params into a template variable map.
// A malformed value (no '=') is ignored rather than failing the preview: a preview is
// advisory, and the submit path validates the REAL values it is given.
func queryVars(c *rux.Context) map[string]string {
	values := c.Req.URL.Query()["var"]
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, kv := range values {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// templateStatus maps the template lookups' sentinels onto HTTP: an unknown project
// and an unknown template are both 404 (there is nothing to read), anything else
// (an invalid name, a template that does not parse) is a 400.
func templateStatus(err error) int {
	switch {
	case errors.Is(err, job.ErrUnknownProject), errors.Is(err, template.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}
