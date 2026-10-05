package job

import "sync"

// jobWatchers is the in-process wake-up table behind WatchJob. It carries no
// payload: a signal only says "something about this job may have changed" and the
// subscriber re-reads what it cares about. Each subscriber channel has capacity 1
// and is signalled non-blockingly, so a burst coalesces into one wake-up and a
// slow subscriber can never stall persist/recordEvent.
type jobWatchers struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// WatchJob subscribes to change signals of one job (status persisted, lifecycle
// event recorded, interaction raised/answered — every one of those writes an event
// or persists the job). The returned cancel func unsubscribes and must be called.
// It is the event-driven replacement of the SSE stream's former DB polling.
func (s *Service) WatchJob(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	w := &s.watchers
	w.mu.Lock()
	if w.subs == nil {
		w.subs = map[string]map[chan struct{}]struct{}{}
	}
	if w.subs[id] == nil {
		w.subs[id] = map[chan struct{}]struct{}{}
	}
	w.subs[id][ch] = struct{}{}
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		if m := w.subs[id]; m != nil {
			delete(m, ch)
			if len(m) == 0 {
				delete(w.subs, id)
			}
		}
		w.mu.Unlock()
	}
}

// signalJob wakes every subscriber of id (no-op when nobody watches it).
func (s *Service) signalJob(id string) {
	w := &s.watchers
	w.mu.Lock()
	for ch := range w.subs[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	w.mu.Unlock()
}
