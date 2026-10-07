package job

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/util"
)

// resumeEnvChainMax bounds the ResumedFrom walk used to collect inherited job env,
// so a corrupt (cyclic) lineage cannot loop.
const resumeEnvChainMax = 32

// resumeEnvFiles is the env_files DECLARATION a continuation inherits from its
// source. Only the file paths are carried (they are non-sensitive and already part
// of the source's request_json); the values are loaded from disk at execution time.
// env_files are local-runner only (Submit rejects them elsewhere), so a source on
// another runner contributes nothing.
func resumeEnvFiles(src JobResult) []string {
	if !config.IsLocalRunnerName(src.Runner) {
		return nil
	}
	var r struct {
		EnvFiles []string `json:"env_files"`
	}
	if src.RequestJSON == "" || json.Unmarshal([]byte(src.RequestJSON), &r) != nil {
		return nil
	}
	return append([]string(nil), r.EnvFiles...)
}

// resumeInheritedEnv resolves, at EXECUTION time, the env a continuation inherits
// from the run(s) it continues: the explicit `env` of every job on the ResumedFrom
// chain (oldest first, so a later link overrides an earlier one). The values are
// read from the source jobs' stored requests and returned for runReq.Env ONLY —
// they are never copied into the continuation's own request / request_json, which
// keeps just the lineage reference (resumed_from). A job without the internal
// ResumeSourceAgent marker (a plain submit that merely names resumed_from) inherits
// nothing: the marker is what proves ResumeJob built the request.
func (s *Service) resumeInheritedEnv(req JobRequest) map[string]string {
	if req.ResumeSourceAgent == "" || req.ResumedFrom == "" {
		return nil
	}
	var chain []map[string]string
	seen := map[string]bool{}
	for id := req.ResumedFrom; id != "" && !seen[id] && len(chain) < resumeEnvChainMax; {
		seen[id] = true
		src, ok := s.Get(id)
		if !ok {
			break
		}
		var r struct {
			Env map[string]string `json:"env"`
		}
		if src.RequestJSON != "" && json.Unmarshal([]byte(src.RequestJSON), &r) == nil && len(r.Env) > 0 {
			chain = append(chain, r.Env)
		}
		id = src.ResumedFrom
	}
	var out map[string]string
	for i := len(chain) - 1; i >= 0; i-- {
		out = util.MergeEnv(out, chain[i])
	}
	return out
}

// resumeAgentEnv is the SOURCE agent's configured env for a continuation carrier:
// the carrier runs as the built-in exec agent, whose own env is not the agent the
// session belongs to (HOME, model variables, credentials paths live on the source
// agent). Resolved from the SAME config snapshot as admission. Nil when the request
// is not a carrier or the source agent is gone.
func resumeAgentEnv(cfg *config.Config, req JobRequest) map[string]string {
	if req.ResumeSourceAgent == "" || req.Agent != agent.ExecAgentKey {
		return nil
	}
	ac, ok := agent.ResolveAgent(cfg, req.ResumeSourceAgent)
	if !ok {
		return nil
	}
	return ac.Env
}

// effectiveOutputAgent is the agent whose OUTPUT configuration (output_format,
// ndjson_*, session_capture, diff policy ...) governs a job. It is the job's own
// agent, except for a continuation carrier: that runs the source agent's CLI out of
// the exec agent, so its stream is the source agent's and must be handled the same
// way as the first turn.
func effectiveOutputAgent(res JobResult) string {
	if res.Agent == agent.ExecAgentKey && res.ResumeAgent != "" {
		return res.ResumeAgent
	}
	return res.Agent
}
