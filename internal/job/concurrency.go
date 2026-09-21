package job

// semaphore returns (creating if needed) the per-project concurrency semaphore.
// A limit <= 0 means unbounded and returns nil (no gating).
func (s *Service) semaphore(projectKey string, limit int) chan struct{} {
	if limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sem, ok := s.sems[projectKey]; ok {
		return sem
	}
	sem := make(chan struct{}, limit)
	s.sems[projectKey] = sem
	return sem
}

// callerSemaphore returns (creating if needed) the per-caller concurrency
// semaphore (E17, design §7.2). Mirrors semaphore(): an empty caller id or a
// limit <= 0 means unbounded and returns nil (no gating). Guarded by s.mu like
// sems. Capacity is frozen at creation (hot-reload caveat, design §7.4).
func (s *Service) callerSemaphore(callerID string, limit int) chan struct{} {
	if callerID == "" || limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sem, ok := s.callerSems[callerID]; ok {
		return sem
	}
	sem := make(chan struct{}, limit)
	s.callerSems[callerID] = sem
	return sem
}

// agentSemaphore returns (creating if needed) the per-agent concurrency semaphore
// (JOB-11, agents.<key>.max_concurrent). Mirrors semaphore()/callerSemaphore(): an
// empty agent key or a limit <= 0 means unbounded and returns nil (no gating). The
// key is the RESOLVED agent of the job (the same key the config and the row carry),
// so a role/template that filled the agent is gated on the agent it actually runs.
func (s *Service) agentSemaphore(agentKey string, limit int) chan struct{} {
	if agentKey == "" || limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sem, ok := s.agentSems[agentKey]; ok {
		return sem
	}
	sem := make(chan struct{}, limit)
	s.agentSems[agentKey] = sem
	return sem
}

// CallerRate exposes the effective per-caller submit rate for the HTTP
// rate-limit middleware (E17, design §7.3). It reads the CURRENT config snapshot
// (s.config(), the atomic.Pointer that Reload swaps), so a SIGHUP change to a
// caller's rate_limit / rate_burst is observed on the very next request — the
// rate-limit config真源 is here, NOT a copy held by httpapi.Server (which is not
// on the reload path). Returns (0, 0) when the caller has no rate gating.
func (s *Service) CallerRate(callerID string) (rps float64, burst int) {
	return s.config().Server.CallerRate(callerID)
}
