package serve

import (
	"sync"
	"time"
)

// sweepClock records when the periodic schedule sweep last FINISHED a pass.
//
// The schedule loop (startScheduleLoop) is the server's single periodic sweeper — the
// cron schedules, the wakeup timers and the pending receipts all ride its tick. The
// health self-check's `sweep` item reads this stamp to notice a loop that stopped
// ticking (a blocked database, a submit that hangs): a table check cannot see that,
// because the tables look fine while nothing advances them.
type sweepClock struct {
	mu sync.Mutex
	at time.Time
}

// Mark records that a pass finished at t.
func (s *sweepClock) Mark(t time.Time) {
	s.mu.Lock()
	s.at = t
	s.mu.Unlock()
}

// LastSweepAt is the finish time of the last completed pass; the zero time means the
// sweeper has not completed one yet.
func (s *sweepClock) LastSweepAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.at
}
