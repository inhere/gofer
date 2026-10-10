package job

// DeleteJob removes a terminal job's durable record (jobstore.DeleteJob keeps the
// job.deleted audit) and then drops its in-memory entry. A job is persisted as
// terminal a moment before finish evicts it (todo / knowledge / health hooks run in
// between), so without the eviction here a delete landing in that window would leave
// Get still answering from memory.
func (s *Service) DeleteJob(id, by string) error {
	if err := s.meta.DeleteJob(id, by); err != nil {
		return err
	}
	if e := s.entry(id); e != nil && isFinished(e.snapshot().Status) {
		s.mu.Lock()
		if s.jobs[id] == e {
			delete(s.jobs, id)
		}
		s.mu.Unlock()
	}
	return nil
}
