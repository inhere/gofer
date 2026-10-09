package jobstore

// ChangeKind names the table family a write touched. The set is exactly what the
// browser's push topics need to invalidate; it is deliberately coarse.
type ChangeKind string

const (
	ChangeJob         ChangeKind = "job"
	ChangeDecision    ChangeKind = "decision"
	ChangeInteraction ChangeKind = "interaction"
	ChangeSession     ChangeKind = "session"
	ChangePlan        ChangeKind = "plan"
	ChangeWorkflow    ChangeKind = "workflow"
	ChangeSchedule    ChangeKind = "schedule"
	ChangeWork        ChangeKind = "work"
)

// Change is one write notification. ID is the job id for job and interaction writes
// (empty for bulk or id-less ones); Status is the job's new status for job writes
// that know it.
type Change struct {
	Kind   ChangeKind
	ID     string
	Status string
}

// ChangeHook receives a Change after a write method returns (it runs from a defer, so
// after the store's write lock was released). It MUST be non-blocking: it is called on
// the writer's goroutine, which in the job service is the recordEvent/persist hot path.
type ChangeHook func(Change)

// SetChangeHook installs (nil clears) the write observer. It is wired once at serve
// assembly; reads of the pointer on every write are lock-free.
func (s *Store) SetChangeHook(h ChangeHook) {
	if h == nil {
		s.changeHook.Store(nil)
		return
	}
	s.changeHook.Store(&h)
}

func (s *Store) emit(c Change) {
	if s == nil {
		return
	}
	if c.Kind == ChangeJob && (c.ID == "" || statsEndStatus(c.Status)) {
		s.statsGen.Add(1) // bulk job writes (delete / prune / narrow updates) and job ends
	}
	if h := s.changeHook.Load(); h != nil {
		(*h)(c)
	}
}
