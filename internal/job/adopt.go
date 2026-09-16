package job

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
)

// adoptLostReason is the error a store-held `recovering` job is ended with when the
// worker process reconnects but does NOT prove it still runs it (RECOV-01 R4): the
// job is absent from the worker's `inflight` report, or the row belongs to a
// different process instance. The text is the fixed vocabulary an operator greps for
// ("worker lost"), matching the hub's own worker_lost family.
const adoptLostReason = "worker lost: job not tracked after restart"

// WorkerInflightJob is one entry of a worker's re-register `inflight` report, projected
// onto the job package's vocabulary: the hub job id the worker still holds and the
// status it reports for it. The projection exists so this package never imports
// wsproto (the assembly layer converts).
type WorkerInflightJob struct {
	JobID  string
	Status string
}

// AdoptBackend is the transport half of an adopted job, supplied by the assembly layer
// that owns the hub connection. The host-side half (log files, status, classify/finish)
// stays in this package; only what needs the wire is injected.
type AdoptBackend struct {
	// Answer sends a host-side interaction answer for jobID back to the worker process
	// so its local job resumes (P2). nil-safe.
	Answer func(jobID, interactionID, answer string)
	// Cancel tells the worker process to tear down jobID: the host cancelled the job
	// while it was adopted, and there is no runner Run left to relay that (RECOV-01 R4).
	// nil-safe.
	Cancel func(jobID string)
}

// ReconcileAdoption implements the hub's RECOV-01 R4 adoption seam: it reconciles the
// jobs THIS store holds in `recovering` for workerID against a worker process that just
// registered with instanceID and reported inflight.
//
// Per store-held row:
//
//   - the row records exactly this process instance AND the worker still reports the
//     job in flight ⇒ ADOPT it: a live handle (log writers + entry) is rebuilt and
//     returned so the hub can attach it to the new connection, hand back the offsets
//     it must rewind to, and return the job to `running`.
//   - anything else (a different/absent instance, or a job the reconnecting worker no
//     longer tracks) ⇒ FAIL it here and now with adoptLostReason. Waiting out the
//     window would only delay a foregone conclusion: no process can ever finish that
//     job. The failed ids are returned for the hub to log.
//
// Adoption also wakes serve's one-shot startup window (AdoptionWake) so the window can
// see whether anything is still unclaimed.
func (s *Service) ReconcileAdoption(workerID, instanceID string, inflight []WorkerInflightJob, backend AdoptBackend) (map[string]*AdoptedJob, []string) {
	if workerID == "" {
		return nil, nil
	}
	recs, err := s.meta.ListRecoveringJobs(workerID)
	if err != nil {
		slog.Warn("job.adopt_list_failed", "event", "job.adopt_list_failed", "component", "server",
			"worker_id", workerID, "err", err)
		return nil, nil
	}
	if len(recs) == 0 {
		return nil, nil
	}
	tracked := make(map[string]struct{}, len(inflight))
	for _, f := range inflight {
		tracked[f.JobID] = struct{}{}
	}

	adopted := make(map[string]*AdoptedJob, len(recs))
	var lost []string
	for _, rec := range recs {
		if instanceID != "" && rec.WorkerInstanceID == instanceID {
			if _, ok := tracked[rec.ID]; ok {
				if aj, ok := s.adoptRecoveringJob(rec, backend); ok {
					adopted[rec.ID] = aj
					continue
				}
			}
		}
		lost = append(lost, rec.ID)
		if n, ferr := s.meta.FailRecoveringJob(rec.ID, s.nowFn().Unix(), adoptLostReason); ferr != nil {
			slog.Warn("job.adopt_fail_failed", "event", "job.adopt_fail_failed", "component", "server",
				"job_id", rec.ID, "err", ferr)
		} else if n > 0 {
			slog.Warn("job.lost_after_restart", "event", "job.lost_after_restart", "component", "server",
				"job_id", rec.ID, "worker_id", workerID, "error_code", "worker_lost", "reason", adoptLostReason)
		}
	}
	s.notifyAdoption()
	return adopted, lost
}

// adoptRecoveringJob rebuilds the live host-side state for ONE store-held recovering
// job: its result-dir log writers (opened in APPEND mode, so the output the previous
// serve mirrored stays and the offsets are its current size), a job entry carrying the
// persisted snapshot, and the cancellable context that makes `job cancel` work on a job
// with no execute goroutine behind it.
//
// It is idempotent: a second adoption of the same job (the previous register's ack
// never landed, so the worker re-registers) returns the EXISTING handle instead of
// creating a second entry and a second set of file handles.
func (s *Service) adoptRecoveringJob(rec jobstore.JobRecord, backend AdoptBackend) (*AdoptedJob, bool) {
	s.mu.Lock()
	if e := s.jobs[rec.ID]; e != nil {
		aj := e.adopted
		s.mu.Unlock()
		return aj, aj != nil
	}
	s.mu.Unlock()

	// The store base is derived from the persisted result dir exactly as TailLog /
	// the log endpoints do (<base>/<jobID> for the legacy layout, which is what
	// filepath.Dir yields); a dated layout still resolves through the same fallback.
	fs := store.NewFileStore(filepath.Dir(rec.ResultDir))
	stdout, stdoutOff, err := fs.AppendLogWriter(rec.ID, store.StreamStdout)
	if err != nil {
		slog.Warn("job.adopt_open_stdout", "event", "job.adopt_open_stdout", "component", "server",
			"job_id", rec.ID, "result_dir", rec.ResultDir, "err", err)
		return nil, false
	}
	stderr, stderrOff, err := fs.AppendLogWriter(rec.ID, store.StreamStderr)
	if err != nil {
		_ = stdout.Close()
		slog.Warn("job.adopt_open_stderr", "event", "job.adopt_open_stderr", "component", "server",
			"job_id", rec.ID, "result_dir", rec.ResultDir, "err", err)
		return nil, false
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &jobEntry{
		result: fromRecord(rec),
		store:  fs,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	aj := &AdoptedJob{
		s:         s,
		entry:     entry,
		jobID:     rec.ID,
		backend:   backend,
		ctx:       ctx,
		cancel:    cancel,
		stdout:    stdout,
		stderr:    stderr,
		stdoutOff: stdoutOff,
		stderrOff: stderrOff,
		seen:      map[string]bool{},
		done:      make(chan struct{}),
	}
	entry.adopted = aj

	s.mu.Lock()
	// Another goroutine may have adopted it while the files were opened: keep the
	// first handle and drop this one (idempotence, not a leak — the writers are closed
	// below).
	if e := s.jobs[rec.ID]; e != nil {
		existing := e.adopted
		s.mu.Unlock()
		cancel()
		_ = stdout.Close()
		_ = stderr.Close()
		return existing, existing != nil
	}
	s.jobs[rec.ID] = entry
	s.mu.Unlock()

	go aj.watchCancel()
	return aj, true
}

// CountRecoveringJobs reports how many jobs the store still holds in `recovering`.
// Serve's one-shot startup window uses it to decide whether anything is left to end
// after an adoption (RECOV-01 R4).
func (s *Service) CountRecoveringJobs() (int, error) {
	return s.meta.CountRecoveringJobs()
}

// AdoptionWake is signalled (non-blocking, capacity 1) whenever an adoption runs, so
// serve's one-shot recovery window can stop early instead of failing jobs a
// reconnecting worker has already taken back.
func (s *Service) AdoptionWake() <-chan struct{} { return s.adoptWake }

// notifyAdoption pings the adoption wake channel without ever blocking: a signal
// already queued means "re-check the recovering count", and the check is idempotent.
func (s *Service) notifyAdoption() {
	select {
	case s.adoptWake <- struct{}{}:
	default:
	}
}

// AdoptedJob is the live handle of one job the server adopted after a serve restart
// (RECOV-01 R4). It plays the same role for an adopted job that internal/runner/worker's
// sink plays for a dispatched one — the hub demuxes that job's frames into it — but
// there is no Run goroutine behind it, so the handle itself owns the pieces Run would
// have: the cancellable context (host cancel), the terminal classify/finish (worker
// Result or worker-lost), and the log file writers the previous process left behind.
type AdoptedJob struct {
	s       *Service
	entry   *jobEntry
	jobID   string
	backend AdoptBackend

	ctx    context.Context
	cancel context.CancelFunc

	stdout io.WriteCloser
	stderr io.WriteCloser

	mu              sync.Mutex
	stdoutOff       int64
	stderrOff       int64
	outcome         *runner.Outcome
	renderedApplied bool
	seen            map[string]bool

	// delivered makes the terminal path run EXACTLY ONCE: a worker replays its Result
	// after reconnecting (the design relies on that replay being idempotent) and a host
	// cancel races it, so the second signal must be dropped rather than finish the job
	// twice (a second event, a second workflow advance).
	delivered atomic.Bool
	// done closes when the terminal path has run, unblocking watchCancel.
	done chan struct{}
}

// WriteLog appends one mirrored log frame to the adopted job's stdout/stderr file and
// advances the server-side offset the hub hands the worker on the next reconnect. A
// failed/short write advances the offset by what actually landed — the offsets must
// never claim bytes the host does not have.
func (a *AdoptedJob) WriteLog(stream string, _ int, text string) {
	if text == "" {
		return
	}
	w, stderr := a.stdout, stream == "stderr"
	if stderr {
		w = a.stderr
	}
	if w == nil {
		return
	}
	n, _ := io.WriteString(w, text)
	if n <= 0 {
		return
	}
	a.mu.Lock()
	if stderr {
		a.stderrOff += int64(n)
	} else {
		a.stdoutOff += int64(n)
	}
	a.mu.Unlock()
}

// Offsets returns the byte counts already persisted for each stream — what the hub
// echoes in the resume ack so the worker replays from exactly the right place.
func (a *AdoptedJob) Offsets() (stdoutOff, stderrOff int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stdoutOff, a.stderrOff
}

// OnOutcome stashes the worker-captured产出 (P4) so the terminal path applies it,
// latest-wins, and mirrors the dispatched path's G1 behaviour: the first frame
// carrying a rendered command is pushed onto the running job at once.
func (a *AdoptedJob) OnOutcome(o runner.Outcome) {
	cp := o
	a.mu.Lock()
	a.outcome = &cp
	fire := o.RenderedCommand != "" && !a.renderedApplied
	if fire {
		a.renderedApplied = true
	}
	a.mu.Unlock()
	if fire {
		a.s.setRunningRenderedCommand(a.entry, a.jobID, o.RenderedCommand)
	}
}

// takeOutcome returns the stashed outcome (nil when the worker sent none) and clears
// it, so a replayed frame after the terminal path cannot resurrect it.
func (a *AdoptedJob) takeOutcome() *runner.Outcome {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.outcome
	a.outcome = nil
	return out
}

// Suspend implements the sink contract for an adopted job whose worker connection
// dropped AGAIN: the job returns to `recovering` under its worker's window and the
// current offsets travel back to the hub. Idempotent for an already-terminal job.
func (a *AdoptedJob) Suspend(reason string) (int64, int64) {
	a.s.setRecovering(a.entry, a.jobID, reason)
	return a.Offsets()
}

// Resume implements the sink contract: the worker process proved it still runs the job,
// so the job returns to `running` and — for an adopted job — the adoption itself is
// logged as the reason it is alive again.
func (a *AdoptedJob) Resume() {
	a.s.clearRecovering(a.entry, a.jobID)
	slog.Info("worker.job_adopted", "event", "worker.job_adopted", "component", "server",
		"job_id", a.jobID, "worker_id", a.entry.snapshot().WorkerID)
	// Re-check serve's one-shot startup window: the row has LEFT `recovering` only now
	// (the ack is on the wire and the sink is attached), so this is the first moment the
	// window can see that there is nothing left for it to end.
	a.s.notifyAdoption()
}

// OnDisconnect finishes an adopted job failed with err: the worker's recovery window
// expired, the worker restarted, or the job is otherwise lost. It runs through the same
// classify/finish path a dispatched job uses, so outcomes, notifications, the jobstore
// row and workflow advancement all behave identically.
func (a *AdoptedJob) OnDisconnect(err error) {
	a.finishTerminal(-1, err)
}

// Finish delivers the worker's authoritative terminal result for an adopted job: it
// collects产出, classifies the outcome against the host context and finishes the job.
// Duplicate results (the worker replays its result after reconnecting) are dropped.
//
// exitCode/err come from the wire projection the assembly layer did (worker status →
// error), mirroring internal/runner/worker's sink exactly.
func (a *AdoptedJob) Finish(exitCode int, err error) {
	a.finishTerminal(exitCode, err)
}

// OnInteraction bridges one worker-raised interaction frame onto the adopted job
// (RECOV-01 R4: the worker keeps running and keeps asking). It mirrors the dispatched
// path: an `open` injects the interaction on the host job (→ pending_interaction) and a
// goroutine waits for the host answer to send it back over the wire via the backend.
// answered/cancelled are state-cleanup actions the host owns; they are ignored.
func (a *AdoptedJob) OnInteraction(action string, raw json.RawMessage) {
	if action != "open" { // the WS action vocabulary (open|answered|cancelled); see runner/worker's bridge
		return
	}
	var it Interaction
	if json.Unmarshal(raw, &it) != nil || it.ID == "" {
		return
	}
	a.mu.Lock()
	if a.seen[it.ID] {
		a.mu.Unlock()
		return
	}
	a.seen[it.ID] = true
	a.mu.Unlock()

	it.JobID = a.jobID
	it.Status = InteractionPending
	it.CreatedAt = a.s.nowFn().Unix()
	if err := a.s.injectInteraction(a.jobID, it); err != nil {
		return
	}
	iid := it.ID
	go func() {
		ans, err := a.s.WaitAnswer(a.ctx, a.jobID, iid)
		if err != nil || ans.Status != InteractionAnswered {
			return
		}
		if a.backend.Answer != nil {
			a.backend.Answer(a.jobID, iid, ans.Answer)
		}
	}()
}

// watchCancel implements the cancel half of the adopted job's lifecycle: with no Run
// goroutine left, the host context is the ONLY signal that a `job cancel` arrived, so
// this watch relays it to the worker process (backend.Cancel) and ends the host job as
// cancelled. It exits as soon as the terminal path has run for any other reason.
func (a *AdoptedJob) watchCancel() {
	select {
	case <-a.done:
		return
	case <-a.ctx.Done():
	}
	if a.delivered.Load() {
		return // finished concurrently (worker result / worker lost); nothing to cancel
	}
	if a.backend.Cancel != nil {
		a.backend.Cancel(a.jobID)
	}
	a.finishTerminal(-1, a.ctx.Err())
}

// finishTerminal runs the ONE terminal path for an adopted job: close the log writers
// (terminal ⇒ handles closed, the invariant every observer relies on), collect产出,
// classify (host cancel/timeout wins over the reported outcome) and finish — the SAME
// service path a dispatched job takes.
func (a *AdoptedJob) finishTerminal(exitCode int, err error) {
	if !a.delivered.CompareAndSwap(false, true) {
		return
	}
	close(a.done)
	_ = a.stdout.Close()
	_ = a.stderr.Close()

	res := runner.Result{ExitCode: exitCode, Err: err, Outcome: a.takeOutcome()}
	a.s.captureOutcomes(a.entry, runner.Request{JobID: a.jobID}, res)
	status, code, runErr := classify(a.ctx, res)
	a.s.finish(a.entry, a.jobID, status, code, runErr)
}
