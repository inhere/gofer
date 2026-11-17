package job

import "github.com/inhere/gofer/internal/agent"

// SessionReloadable reports whether the persistent session res can be reloaded after the
// process that runs it exits: it has an agent session id, its agent advertised session/load
// and the config does not turn it off (the test `job resume` makes), and it runs in the shared checkout rather than in a managed
// worktree or temporary checkout a re-dispatch would have to recreate. A worker answers it
// for its own local session rows, to decide whether an upgrade has to wait for the session;
// the hub-side counterpart is sessionRedispatchable, and the two must agree.
func (s *Service) SessionReloadable(res JobResult) bool {
	if !res.Session || res.SessionID == "" || !res.SessionLoadable || res.WorktreePath != "" || res.Interactive {
		return false
	}
	ac, ok := agent.ResolveAgent(s.config(), res.Agent)
	return ok && ac.Type == agent.TypeACPAgent && ac.ACP.AllowsLoadSession()
}
