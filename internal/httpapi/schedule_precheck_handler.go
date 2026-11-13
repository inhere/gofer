package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
)

// acceptSchedulePrecheck is the HTTP side of a schedule's pre-run check (design
// 2026-11-12 §2): a non-empty declaration needs `schedule.precheck` (asked only now — a
// request without one, or one that clears it, needs nothing extra), must be well
// formed, and is only allowed for a schedule that runs on this server (runner is not a
// worker or peer: the check runs here). It answers the refusal itself and reports
// whether the request may go on.
func (s *Server) acceptSchedulePrecheck(c *rux.Context, pc *job.SchedulePrecheck, runner string) bool {
	if !s.can(c, authz.PermSchedulePrecheck) {
		s.denyScheduleAction(c, authz.PermSchedulePrecheck)
		return false
	}
	if err := pc.Validate(); err != nil {
		writeError(c, http.StatusBadRequest, "invalid schedule precheck", err.Error())
		return false
	}
	if job.IsRemoteRunner(s.jobs.Config(), runner) {
		writeError(c, http.StatusBadRequest, "invalid schedule precheck",
			fmt.Sprintf("a pre-run check runs on the server, so runner %q (a remote runner) cannot have one", runner))
		return false
	}
	return true
}

// patchPrecheckField reads the optional `precheck` key of a PATCH body: absent = leave it
// alone; null or an object without argv = clear it; otherwise the new declaration.
func patchPrecheckField(raw json.RawMessage) (present bool, pc *job.SchedulePrecheck, err error) {
	if len(raw) == 0 {
		return false, nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true, nil, nil
	}
	var decoded job.SchedulePrecheck
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return true, nil, fmt.Errorf("precheck must be an object {argv, timeout_sec, skip_unchanged}: %w", err)
	}
	if !decoded.IsSet() {
		return true, nil, nil
	}
	return true, &decoded, nil
}
