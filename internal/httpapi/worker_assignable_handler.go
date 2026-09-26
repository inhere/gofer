package httpapi

import (
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// assignableProject is one project a given worker could be dispatched to, as the
// worker-config wizard needs it: the key (what a `roots` mapping is registered for)
// and the server-side host_path (the logical prefix the worker has to map onto one
// of its own roots).
type assignableProject struct {
	Key      string `json:"key"`
	HostPath string `json:"host_path,omitempty"`
}

// workerAssignableResp is GET /v1/workers/{id}/assignable's body (CFG-05): the
// projects whose allowed_runners may route to this worker, plus the server and wire
// protocol versions so `gofer worker init` can report what it is talking to.
type workerAssignableResp struct {
	WorkerID        string              `json:"worker_id"`
	Projects        []assignableProject `json:"projects"`
	ServerVersion   string              `json:"server_version,omitempty"`
	ProtocolVersion int                 `json:"protocol_version"`
}

// handleWorkerAssignable answers "which projects can be dispatched to this worker?" —
// the one question the worker wizard cannot answer locally (the projects live on the
// server). Read-only; the answer is derived from the live project registry.
//
// Auth (design §二): the worker's OWN token or any user caller. Another worker's
// token is refused — a worker has no business enumerating its peers' assignment — and
// the check is deliberately NOT callerMayAdmin, because a worker id never carries
// can_admin (see buildCallers) and this endpoint must stay usable by the worker it
// describes.
func (s *Server) handleWorkerAssignable(c *rux.Context) {
	workerID := c.Param("id")
	if workerID == "" {
		writeError(c, http.StatusBadRequest, "missing worker id", "path must be /v1/workers/{id}/assignable")
		return
	}
	if callerKindFromCtx(c) == callerKindWorker && callerFromCtx(c) != workerID {
		writeError(c, http.StatusForbidden, "not this worker's assignment",
			"only worker "+workerID+"'s own token (or a user caller) may read its assignable projects")
		return
	}
	c.JSON(http.StatusOK, workerAssignableResp{
		WorkerID:        workerID,
		Projects:        s.assignableProjects(workerID),
		ServerVersion:   s.build.DisplayVersion(),
		ProtocolVersion: wsproto.CurrentProtocolVersion,
	})
}

// assignableProjects lists the projects that can be dispatched to workerID, sorted by
// key (project.Registry.List is already sorted) and normalised to an empty slice so
// the JSON shows [] rather than null.
func (s *Server) assignableProjects(workerID string) []assignableProject {
	out := []assignableProject{}
	if s.projects == nil {
		return out
	}
	for _, key := range s.projects.List() {
		p, err := s.projects.Get(key)
		if err != nil {
			continue
		}
		if !projectDispatchableTo(p.AllowedRunners, s.runners, workerID) {
			continue
		}
		out = append(out, assignableProject{Key: key, HostPath: p.HostPath})
	}
	return out
}

// projectDispatchableTo reports whether a project's `allowed_runners` can route a job
// to this worker. Two shapes count, because both are in use:
//
//   - the worker id itself is listed — the common case, where the worker's runner is
//     named after the worker (runner key == worker id);
//   - a listed runner is a `type: worker` runner pinned to it (`worker_id: <id>`),
//     which is the same admission rule the job service applies on submit
//     (internal/job.isWorkerRunner + checkRunnerAllowed).
//
// A local-only project (or one assigned to another worker) is not listed.
func projectDispatchableTo(allowed []string, runners map[string]config.RunnerConfig, workerID string) bool {
	for _, name := range allowed {
		if name == workerID {
			return true
		}
		// "worker" mirrors internal/job.isWorkerRunner: runner type "worker" is the
		// ws-worker dispatch runner, and its WorkerID is the target.
		if rc, ok := runners[name]; ok && rc.Type == "worker" && rc.WorkerID == workerID {
			return true
		}
	}
	return false
}
