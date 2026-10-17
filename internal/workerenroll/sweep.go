package workerenroll

import (
	"log/slog"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Sweep timing (design 「状态机与并发」).
const (
	DefaultSweepInterval = 5 * time.Second
	// AcceptingStaleSec is how long a request may sit in accepting before the sweep
	// treats the accepting call as crashed and reconciles it.
	AcceptingStaleSec = 30
	// RetainEndedDays is how long ended requests are kept before deletion.
	RetainEndedDays = 30
)

// SweepResult counts what one pass did (tests / logs).
type SweepResult struct {
	Expired    int
	Reconciled int
	Pruned     int
}

// Sweep is one maintenance pass. It deliberately ignores Settings.Enabled: requests
// left over from before the feature was switched off must still expire (and an
// accepted-but-uncollected registration must still be revoked); only new requests are
// refused. startup=true reconciles every accepting row regardless of age.
func (s *Service) Sweep(startup bool) (SweepResult, error) {
	var res SweepResult
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	rows, err := s.st.ListWorkerEnrollmentsByState(jobstore.EnrollPending, jobstore.EnrollAccepting, jobstore.EnrollAccepted)
	if err != nil {
		return res, err
	}
	now := s.now()
	for _, e := range rows {
		switch e.State {
		case jobstore.EnrollAccepting:
			if startup || now-e.DecidedAt >= AcceptingStaleSec {
				done, err := s.reconcileAccepting(e)
				note(err)
				if done {
					res.Reconciled++
				}
			}
		default:
			done, err := s.Expire(e.ID)
			note(err)
			if done {
				res.Expired++
			}
		}
	}
	cutoff := now - int64(RetainEndedDays*24*time.Hour/time.Second)
	n, err := s.st.DeleteEndedWorkerEnrollmentsBefore(cutoff)
	note(err)
	res.Pruned = n
	return res, firstErr
}

// Reconcile is the serve-start pass: Sweep with every accepting row reconciled.
func (s *Service) Reconcile() (SweepResult, error) { return s.Sweep(true) }

// reconcileAccepting settles a request stuck in accepting. If the config already holds
// the worker with the issued token's fingerprint the registration happened, so the
// request finishes as accepted with a fresh collect deadline; otherwise it goes back to
// pending while its decision window is open, or to expired.
func (s *Service) reconcileAccepting(e jobstore.WorkerEnrollment) (bool, error) {
	if token, ok := s.cfg.WorkerToken(e.WorkerID); ok && e.TokenFP != "" && fpEqual(config.WorkerTokenFingerprint(token), e.TokenFP) {
		won, err := s.st.FinishWorkerEnrollmentAccept(e.ID, s.now()+s.cfg.Enrollment().collect())
		if err == nil && won {
			s.audit.Record(AuditAccept, e.ID, systemActor)
		}
		return won, err
	}
	if s.now() < e.DecideUntil {
		return s.st.RevertWorkerEnrollmentAccept(e.ID)
	}
	won, err := s.st.TransitionWorkerEnrollment(e.ID, []string{jobstore.EnrollAccepting}, jobstore.EnrollExpired, s.now(), systemActor, "accept did not complete")
	if err == nil && won {
		s.audit.Record(AuditExpire, e.ID, systemActor)
	}
	return won, err
}

// Sweeper runs Service.Sweep on a timer until Close.
type Sweeper struct {
	svc      *Service
	interval time.Duration
	stop     chan struct{}
	wg       sync.WaitGroup
	once     sync.Once
}

// NewSweeper builds a sweeper; interval <= 0 means DefaultSweepInterval.
func NewSweeper(svc *Service, interval time.Duration) *Sweeper {
	if interval <= 0 {
		interval = DefaultSweepInterval
	}
	return &Sweeper{svc: svc, interval: interval, stop: make(chan struct{})}
}

// Start launches the loop. It is safe to call once.
func (w *Sweeper) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(w.interval)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-t.C:
				if _, err := w.svc.Sweep(false); err != nil {
					slog.Warn("workerenroll.sweep", "event", "workerenroll.sweep", "err", err)
				}
			}
		}
	}()
}

// Close stops the loop and waits for an in-flight pass (call before closing the store).
func (w *Sweeper) Close() {
	w.once.Do(func() { close(w.stop) })
	w.wg.Wait()
}

// Collector turns hub presence events into MarkCollectedByWorker calls on its own
// goroutine. Observe runs on the hub's connection goroutine, so it only enqueues.
type Collector struct {
	svc   *Service
	queue chan string
	stop  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

// NewCollector starts the worker goroutine; size is the queue bound (<=0: 128).
func NewCollector(svc *Service, size int) *Collector {
	if size <= 0 {
		size = 128
	}
	c := &Collector{svc: svc, queue: make(chan string, size), stop: make(chan struct{})}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.stop:
				return
			case id := <-c.queue:
				if _, err := c.svc.MarkCollectedByWorker(id); err != nil {
					slog.Warn("workerenroll.collect", "event", "workerenroll.collect", "worker", id, "err", err)
				}
			}
		}
	}()
	return c
}

// Observe is a wshub presence observer: online events are queued without blocking;
// a full queue drops the event with a log line (the confirm call or the collect-window
// expiry still settles the request).
func (c *Collector) Observe(workerID string, online bool) {
	if !online {
		return
	}
	select {
	case c.queue <- workerID:
	default:
		slog.Warn("workerenroll.collect_queue_full", "event", "workerenroll.collect_queue_full", "worker", workerID)
	}
}

// Close stops the goroutine and waits for it.
func (c *Collector) Close() {
	c.once.Do(func() { close(c.stop) })
	c.wg.Wait()
}
