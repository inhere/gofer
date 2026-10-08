package job

import (
	"log/slog"
	"time"
)

// submitSlowThreshold is the total Submit duration above which a single
// job.submit_slow warning lists the per-phase timings (gofer-5foz). A var so tests
// can lower it.
var submitSlowThreshold = 2 * time.Second

// phaseTimer records how long each named phase of a synchronous path took. The
// zero cost when fast: a few time.Now calls and no allocation beyond the phase slice.
type phaseTimer struct {
	start  time.Time
	last   time.Time
	phases []phaseTiming
}

type phaseTiming struct {
	name string
	d    time.Duration
}

func newPhaseTimer() *phaseTimer {
	now := time.Now()
	return &phaseTimer{start: now, last: now}
}

// mark attributes the time since the previous mark (or the start) to name.
func (t *phaseTimer) mark(name string) {
	now := time.Now()
	t.phases = append(t.phases, phaseTiming{name: name, d: now.Sub(t.last)})
	t.last = now
}

// report logs one warn when the total exceeded threshold: total_ms plus one
// <phase>_ms attribute per phase, in execution order. event names the log message.
func (t *phaseTimer) report(event string, threshold time.Duration, attrs ...any) {
	total := time.Since(t.start)
	if total < threshold {
		return
	}
	args := append([]any{}, attrs...)
	args = append(args, "total_ms", total.Milliseconds())
	for _, p := range t.phases {
		args = append(args, p.name+"_ms", p.d.Milliseconds())
	}
	slog.Warn(event, args...)
}
