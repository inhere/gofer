package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// trackerSyncJobTimeoutSec bounds the dispatched `repo sync` (the manual CLI default is 1m).
const trackerSyncJobTimeoutSec = 120

// handleTrackerRepoSync is TRK-05: a person asks the server to run `gofer repo sync` in
// a mirrored repository. The server picks the runner that last pushed the repo (else the
// project's default runner), derives the repo directory relative to that project and
// submits a hidden exec job (tag tracker-sync) carrying the tracker id, so the job's own
// credential may call /v1/tracker/sync for exactly that tracker. The job id is returned;
// the web polls it and the repo's last_sync_at.
func (s *Server) handleTrackerRepoSync(c *rux.Context) {
	if s.trackerStore == nil || s.jobs == nil || s.projects == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "")
		return
	}
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "only a person may trigger a tracker sync",
			"job, steward and worker credentials cannot dispatch `repo sync`; the sync job itself runs under its own credential")
		return
	}
	trackerID := c.Param("tracker_id")
	repo, ok := s.findTrackerRepo(trackerID)
	if !ok {
		writeError(c, http.StatusNotFound, "tracker repo not found", trackerID)
		return
	}
	cfg := s.projects.Config()
	if cfg == nil {
		writeError(c, http.StatusServiceUnavailable, "config unavailable", "")
		return
	}
	// The tracker's own project_key is a label the repository chose; when it does not
	// name a registered project, the repository's path decides.
	projectKey := repo.ProjectKey
	if _, known := cfg.Projects[projectKey]; !known {
		projectKey = matchProjectByPath(cfg, repo.RelPath)
	}
	proj, known := cfg.Projects[projectKey]
	if projectKey == "" || !known {
		writeError(c, http.StatusConflict, "tracker repo has no project",
			"the repository is not attributed to a registered project; set project_key in .gofer/tracker/config.yaml and sync once from its directory")
		return
	}
	runnerKey := config.NormalizeRunnerName(repo.SourceRunner)
	if runnerKey == "" {
		runnerKey = defaultProjectRunner(proj)
	}
	cwd, ok := tracker.RepoCwd(repo.RelPath, s.projectRootViews(cfg, proj, projectKey, runnerKey))
	if !ok {
		writeError(c, http.StatusConflict, "repository outside the project",
			fmt.Sprintf("the last synced path %q is not under project %s on runner %s", repo.RelPath, projectKey, runnerKey))
		return
	}
	res, err := s.jobs.Submit(job.JobRequest{
		ProjectKey: projectKey, Agent: agent.ExecAgentKey, Runner: runnerKey,
		Cmd: []string{"gofer", "repo", "sync"}, Cwd: cwd,
		Title: "tracker sync · " + trackerID, Tags: []string{job.TrackerSyncJobTag},
		TrackerID: trackerID, TimeoutSec: trackerSyncJobTimeoutSec, CallerID: callerFromCtx(c),
	})
	if err != nil {
		writeError(c, submitStatus(err), "tracker sync job rejected", err.Error())
		return
	}
	c.JSON(http.StatusAccepted, map[string]any{"job_id": res.ID, "tracker_id": trackerID, "project_key": projectKey, "runner": runnerKey, "cwd": cwd})
}

func (s *Server) findTrackerRepo(trackerID string) (jobstore.TrackerRepo, bool) {
	repos, err := s.trackerStore.ListTrackerRepos()
	if err != nil {
		return jobstore.TrackerRepo{}, false
	}
	for _, r := range repos {
		if r.TrackerID == trackerID {
			return r, true
		}
	}
	return jobstore.TrackerRepo{}, false
}

// projectRootViews lists the project's root as each runner/path view sees it, the
// runner's own view first.
func (s *Server) projectRootViews(cfg *config.Config, proj config.ProjectConfig, projectKey, runnerKey string) []string {
	var roots []string
	if config.IsLocalRunnerName(runnerKey) {
		roots = append(roots, cfg.ExecPath(proj))
	} else if dir := s.workerProjectDir(runnerKey, projectKey); dir != "" {
		roots = append(roots, dir)
	}
	return append(roots, proj.HostPath, proj.ContainerPath)
}

// defaultProjectRunner is the runner a project uses when nothing names one: the
// built-in local runner, unless the project's allowed_runners leaves it out.
func defaultProjectRunner(proj config.ProjectConfig) string {
	if len(proj.AllowedRunners) == 0 {
		return config.BuiltinLocalRunner
	}
	for _, r := range proj.AllowedRunners {
		if config.IsLocalRunnerName(r) {
			return config.BuiltinLocalRunner
		}
	}
	return config.NormalizeRunnerName(strings.TrimSpace(proj.AllowedRunners[0]))
}

// matchProjectByPath finds the project whose root contains path (the longest root wins),
// "" when none does.
func matchProjectByPath(cfg *config.Config, path string) string {
	best, bestLen := "", -1
	for key, p := range cfg.Projects {
		for _, root := range []string{p.HostPath, p.ContainerPath} {
			if root == "" {
				continue
			}
			if _, ok := tracker.RepoCwd(path, []string{root}); ok && len(root) > bestLen {
				best, bestLen = key, len(root)
			}
		}
	}
	return best
}

// handleTrackerRepoRename moves a mirrored repository from a legacy UUID tracker_id to
// its derived short id (POST /v1/tracker/repos/{tracker_id}/rename {"new_tracker_id"}).
// The new id must equal tracker.ShortTrackerID(old), so a caller cannot rename onto an
// arbitrary id. A person may rename any repo; a job credential only the tracker its job
// is associated with. Idempotent: absent old id is a 200 no-op; both present is 409.
//
// DEPRECATED(v0.126): remove in v0.129 (legacy UUID tracker_id migration).
func (s *Server) handleTrackerRepoRename(c *rux.Context) {
	if s.trackerStore == nil {
		writeError(c, http.StatusServiceUnavailable, "tracker mirror unavailable", "")
		return
	}
	oldID := c.Param("tracker_id")
	var req struct {
		NewTrackerID string `json:"new_tracker_id"`
	}
	if err := c.BindJSON(&req); err != nil || req.NewTrackerID == "" {
		writeError(c, http.StatusBadRequest, "new_tracker_id required", "")
		return
	}
	if oldID == "" || req.NewTrackerID != tracker.ShortTrackerID(oldID) {
		writeError(c, http.StatusBadRequest, "new_tracker_id must be the derived short id of the old one", "")
		return
	}
	switch callerKindFromCtx(c) {
	case callerKindUser:
	default:
		jc, isJob := jobCallerFromCtx(c)
		if !isJob || jc.isSteward() {
			writeError(c, http.StatusForbidden, "tracker rename not allowed for this credential", "")
			return
		}
		snap, ok := job.JobResult{}, false
		if s.jobs != nil {
			snap, ok = s.jobs.Get(jc.JobID)
		}
		if !ok || snap.TrackerID != oldID {
			writeError(c, http.StatusForbidden, "job credential may not rename this tracker",
				"a job may only rename the tracker it is associated with (tracker_id on the job)")
			return
		}
	}
	renamed, err := s.trackerStore.RenameTracker(oldID, req.NewTrackerID)
	if errors.Is(err, jobstore.ErrTrackerRenameConflict) {
		writeError(c, http.StatusConflict, "tracker rename conflict", "both the old and the new tracker_id already exist on the server")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "tracker rename failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"tracker_id": req.NewTrackerID, "old_tracker_id": oldID, "renamed": renamed})
}
