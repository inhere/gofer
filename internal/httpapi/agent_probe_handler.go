package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
)

// probeReq is the POST /v1/agents/{id}/probe body; both fields are optional.
type probeReq struct {
	// Project names the project the probe runs in. Empty = the first project that
	// admits the agent (sorted, so the choice is stable).
	Project string `json:"project"`
	// TimeoutSec bounds the probe (0 = DefaultProbeTimeoutSec): it is the job's own
	// deadline, so a hung agent CLI cannot leave the caller waiting.
	TimeoutSec int `json:"timeout_sec"`
}

// agentProbeResult is the probe's answer: the ordinary job that carried it, its
// outcome, and the first line the agent produced — enough for a badge tooltip or a
// terminal one-liner without a second request.
type agentProbeResult struct {
	JobID      string `json:"job_id"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	FirstLine  string `json:"first_line,omitempty"`
}

const (
	// probePrompt is the fixed instruction a probe carries (SUP-01 P3): one line of
	// output and nothing else, so probing never touches a checkout or a plan.
	probePrompt = "只回复一行 OK，不要做任何其他事情。"
	// DefaultProbeTimeoutSec is a probe's deadline (and the sync wait around it) when
	// the caller asks for none: long enough for a cold CLI start, short enough that a
	// hung one is reported as a failure rather than as silence.
	DefaultProbeTimeoutSec = 120
)

// handleProbeAgent submits a probe job for one agent (SUP-01 P3): it goes through the
// NORMAL submit path (admission, runner, agent argv, result dir), so the probe proves
// exactly what a real job would hit — and its outcome counts towards the agent's
// health like any other job.
//
//   - unknown agent → 404 (there is nothing to run);
//   - no project admits the agent (and none was named) → 400, never a job that would
//     be rejected on the way out;
//   - an exec agent is probed with a trivial argv of its own (an exec request carries
//     its command, so there is no CLI to ask).
func (s *Server) handleProbeAgent(c *rux.Context) {
	// The route param is spelled {id} because rux allows only one name per path
	// position and the presence routes already own it; the value is an agent key.
	key := strings.TrimSpace(c.Param("id"))
	ac, ok := s.agents.Get(key)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown agent", "no agent "+key)
		return
	}
	var body probeReq
	if raw, _ := io.ReadAll(c.Req.Body); len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
			return
		}
	}

	cfg := s.jobs.Config()
	project := strings.TrimSpace(body.Project)
	if project == "" {
		project = firstProjectAdmitting(cfg, key)
		if project == "" {
			writeError(c, http.StatusBadRequest, "no project admits this agent",
				"name one with the project field, or add the agent to a project's allowed_agents")
			return
		}
	} else if err := agent.CheckAllowed(cfg, project, key); err != nil {
		writeError(c, http.StatusBadRequest, "project does not admit this agent", err.Error())
		return
	}
	timeout := body.TimeoutSec
	if timeout <= 0 {
		timeout = DefaultProbeTimeoutSec
	}

	req := job.JobRequest{
		ProjectKey: project,
		Agent:      key,
		Runner:     probeRunner(cfg.Projects[project]),
		Prompt:     probePrompt,
		Tags:       []string{"probe"},
		Title:      "probe " + key,
		TimeoutSec: timeout,
		// Wait a little past the job's own deadline so the caller gets the terminal
		// result (reported as a timeout) instead of an async stub.
		WaitTimeoutSec: timeout + 30,
		CallerID:       callerFromCtx(c),
		Client:         clientIP(c),
	}
	if ac.Type == agent.TypeExec {
		req.Cmd = execProbeCmd()
	}
	res, _, err := s.jobs.SubmitSync(req, true)
	if err != nil {
		writeError(c, submitStatus(err), "probe rejected", err.Error())
		return
	}

	out := agentProbeResult{JobID: res.ID, Status: res.Status, ExitCode: res.ExitCode}
	if res.EndedAt > res.StartedAt {
		out.DurationMs = (res.EndedAt - res.StartedAt) * 1000
	}
	if b, err := s.jobs.TailLog(res.ID, store.StreamStdout, 8192); err == nil {
		out.FirstLine = firstLine(string(b))
	}
	c.JSON(http.StatusOK, out)
}

// firstProjectAdmitting returns the first (sorted) project whose allowed_agents admits
// the agent, or "" when none does. Sorted so the default project of a probe is stable
// across requests and processes.
func firstProjectAdmitting(cfg *config.Config, agentKey string) string {
	if cfg == nil {
		return ""
	}
	keys := make([]string, 0, len(cfg.Projects))
	for k := range cfg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if agent.CheckAllowed(cfg, k, agentKey) == nil {
			return k
		}
	}
	return ""
}

// probeRunner picks the runner a probe runs on: the project's own first allowed runner
// (the built-in "local" when the list is empty, which is what an empty allowlist
// admits). A worker-only project is therefore probed on the box that will run its real
// work, not on this host.
func probeRunner(proj config.ProjectConfig) string {
	if len(proj.AllowedRunners) == 0 {
		return "local"
	}
	return proj.AllowedRunners[0]
}

// execProbeCmd is the argv an EXEC agent is probed with. An exec request's argv
// belongs to the caller, so there is no CLI to ask: the probe proves the job path
// itself (result dir, logs, timeout) and produces the same one-line answer.
func execProbeCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "echo OK"}
	}
	return []string{"echo", "OK"}
}

// firstLine returns the first non-empty line of a log tail, trimmed and bounded — the
// one-line evidence a probe reports.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[:200]
		}
		return line
	}
	return ""
}
