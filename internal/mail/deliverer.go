package mail

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// The server-side deliverer (design 2026-10-16「二、gofer 信箱」/「投递器」, plan M5): a periodic
// sweep inside the server that does what no hook can — reaching recipients that are not
// running a turn.
//
//   - It returns letters whose claim lease ran out to pending and parks letters past their
//     delivery deadline in the capture box (held).
//   - It wakes idle Claude Code / codex sessions that have pending letters with ONE short wake
//     prompt (<gofer-mail-wake/>), so a turn starts and the hook injects the letters at its
//     beginning. The wake travels the relay's transports but is not a web message (see
//     sessionrelay.Wake): never Say, no outbox row, no web prefix.
//   - It delivers the letter BODY itself to suga sessions (`suga send`) and to ACP session
//     jobs waiting for input (`job say`), wrapped in the same <gofer-mail> tag the hook
//     renders. Those server-side paths are at-least-once: claim (lease) → send → confirm; a
//     failure after the send re-delivers the letter, and the tag carries the id so the
//     recipient can recognise the repeat.
//   - A letter to a pty session job can never be delivered: it is held at once.
//
// A session that comes back (re-register / heartbeat after being offline) needs nothing
// here: its heartbeat answer carries mail_pending, and the hook injects the letters at the
// next SessionStart / UserPromptSubmit like for any other session.

// Delivery numbers (设计「默认数值」). Settings overrides them from the server config.
const (
	// DefaultTick is how often the deliverer sweeps.
	DefaultTick = 30 * time.Second
	// DefaultWakeIdle is the idle age (now - last_seen_at) a session needs before it is woken.
	DefaultWakeIdle = 5 * time.Minute
	// DefaultWakeInterval is the minimum gap between two wakes of one session.
	DefaultWakeInterval = 10 * time.Minute
	// DefaultLease is the claim lease of the deliverer's own claims.
	DefaultLease = jobstore.DefaultMailClaimTTL

	// sugaRetryAfter is how long a suga session that did not take a letter (not online, send
	// failed) is left alone before the next attempt.
	sugaRetryAfter = 2 * time.Minute
	// deliverBatch bounds the letters one session gets per sweep over the server-side path.
	deliverBatch = 5
	// deliverBodyRunes is the per-letter body budget of a server-side delivery (the same
	// 8000 characters the hook injects).
	deliverBodyRunes = 8000
	// sendTimeout bounds one wake / letter send, on top of the transport's own windows.
	sendTimeout = 2 * time.Minute
	// listLimit is how many pending letters one sweep looks at.
	listLimit = jobstore.MailListMaxLimit
	// delivererBy is the speaker label the transports stamp on the jobs they run.
	delivererBy = "gofer-mail"
	// PtyHoldReason is why a letter to a pty session job is parked.
	PtyHoldReason = "pty session jobs cannot receive mail"
)

// Settings are the deliverer's timing numbers (config mail.*).
type Settings struct {
	Tick         time.Duration
	WakeIdle     time.Duration
	WakeInterval time.Duration
	Lease        time.Duration
}

// DefaultSettings returns the design's defaults.
func DefaultSettings() Settings {
	return Settings{Tick: DefaultTick, WakeIdle: DefaultWakeIdle, WakeInterval: DefaultWakeInterval, Lease: DefaultLease}
}

func (s Settings) withDefaults() Settings {
	d := DefaultSettings()
	if s.Tick <= 0 {
		s.Tick = d.Tick
	}
	if s.WakeIdle <= 0 {
		s.WakeIdle = d.WakeIdle
	}
	if s.WakeInterval <= 0 {
		s.WakeInterval = d.WakeInterval
	}
	if s.Lease <= 0 {
		s.Lease = d.Lease
	}
	return s
}

// SessionSender is the relay's raw send seam; *sessionrelay.Service satisfies it.
type SessionSender interface {
	// DeliverLetter hands text to the session's live process through its agent's
	// deliver_command (exit 3 = ReasonNotRunning). by is the display label, origin the
	// caller-scope ceiling owner of the delivery (the letter's sender).
	DeliverLetter(ctx context.Context, sid, text, by, origin string) (sessionrelay.RawResult, error)
	// Wake sends the wake prompt through the relay's transports without any web semantics.
	Wake(ctx context.Context, sid, text, by, origin string) (sessionrelay.RawResult, error)
}

// JobSayer is the ACP session job's input seam; *job.Service satisfies it.
type JobSayer interface {
	SaySession(id, message string) error
}

// DelivererOptions assembles a Deliverer. Svc is the mailbox the HTTP server uses too.
type DelivererOptions struct {
	Svc    *Service
	Sender SessionSender
	Jobs   JobSayer
	// IsSuga says whether a session's agent key is suga (its letters go to `suga send`);
	// nil = the key is "suga".
	IsSuga   func(agentKey string) bool
	Settings Settings
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Deliverer is the sweep itself. Start launches the loop, Stop / Drain end it.
type Deliverer struct {
	svc    *Service
	sender SessionSender
	jobs   JobSayer
	isSuga func(string) bool
	now    func() time.Time

	mu       sync.Mutex
	settings Settings
	wokeAt   map[string]time.Time // session id → last wake attempt
	sugaNext map[string]time.Time // session id → earliest next suga attempt

	// tickMu serialises sweeps (the loop's and a direct Tick's).
	tickMu sync.Mutex

	lifeMu  sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

// NewDeliverer builds the deliverer (not started).
func NewDeliverer(o DelivererOptions) *Deliverer {
	d := &Deliverer{
		svc: o.Svc, sender: o.Sender, jobs: o.Jobs, isSuga: o.IsSuga, now: o.Now,
		settings: o.Settings.withDefaults(),
		wokeAt:   map[string]time.Time{}, sugaNext: map[string]time.Time{},
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.isSuga == nil {
		d.isSuga = func(key string) bool { return strings.TrimSpace(key) == "suga" }
	}
	return d
}

// Configure replaces the timing numbers (config hot reload); zero fields take the defaults.
func (d *Deliverer) Configure(s Settings) {
	d.mu.Lock()
	d.settings = s.withDefaults()
	d.mu.Unlock()
}

func (d *Deliverer) current() Settings {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.settings
}

// Start launches the sweep loop (idempotent). The loop ends at Stop / Drain.
func (d *Deliverer) Start() {
	d.lifeMu.Lock()
	defer d.lifeMu.Unlock()
	if d.started {
		return
	}
	d.started = true
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.done = make(chan struct{})
	go d.loop(ctx, d.done)
}

func (d *Deliverer) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		d.Tick(ctx)
		timer := time.NewTimer(d.current().Tick)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Drain stops the loop and waits for a sweep in flight to finish, or for ctx. Safe to call
// more than once, and before Start.
func (d *Deliverer) Drain(ctx context.Context) error {
	d.lifeMu.Lock()
	cancel, done := d.cancel, d.done
	d.lifeMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop ends the loop and waits (bounded) for it.
func (d *Deliverer) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Drain(ctx); err != nil {
		slog.Warn("mail deliverer: stop timed out", "error", err)
	}
}

// Tick runs one sweep now.
func (d *Deliverer) Tick(ctx context.Context) {
	d.tickMu.Lock()
	defer d.tickMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	now := d.now()
	st := d.current()
	store := d.svc.store
	if _, err := store.ReleaseExpiredClaims(now.Unix()); err != nil {
		slog.Warn("mail deliverer: release expired claims failed", "error", err)
	}
	held, err := store.ExpireOverdue(now.Unix())
	if err != nil {
		slog.Warn("mail deliverer: expire overdue failed", "error", err)
	}
	if err := d.svc.RecordHeld(held); err != nil {
		slog.Warn("mail deliverer: recording overdue letters failed", "error", err)
	}
	d.sweepSessions(ctx, now, st)
	d.sweepJobs(ctx, now, st)
	d.sweepAgents(ctx, now, st)
}

// pendingBy lists the pending letters to one kind of recipient, grouped by recipient in the
// order they first appear (urgent letters first within a recipient).
func (d *Deliverer) pendingBy(toKind string) (ids []string, byID map[string][]jobstore.Mail, err error) {
	list, err := d.svc.store.ListMail(jobstore.ListMailOpts{
		ToKind: toKind, States: []string{jobstore.MailPending}, OldestFirst: true, Limit: listLimit,
	})
	if err != nil {
		return nil, nil, err
	}
	byID = map[string][]jobstore.Mail{}
	for _, m := range list {
		if _, seen := byID[m.ToID]; !seen {
			ids = append(ids, m.ToID)
		}
		byID[m.ToID] = append(byID[m.ToID], m)
	}
	for _, id := range ids {
		urgent := make([]jobstore.Mail, 0, len(byID[id]))
		rest := make([]jobstore.Mail, 0, len(byID[id]))
		for _, m := range byID[id] {
			if m.Urgent {
				urgent = append(urgent, m)
			} else {
				rest = append(rest, m)
			}
		}
		byID[id] = append(urgent, rest...)
	}
	return ids, byID, nil
}

func (d *Deliverer) sweepSessions(ctx context.Context, now time.Time, st Settings) {
	ids, byID, err := d.pendingBy(jobstore.MailKindSession)
	if err != nil {
		slog.Warn("mail deliverer: list pending session mail failed", "error", err)
		return
	}
	for _, sid := range ids {
		if ctx.Err() != nil {
			return
		}
		a, ok, err := d.svc.store.GetAgentSession(sid)
		if err != nil || !ok {
			continue
		}
		letters := byID[sid]
		if d.isSuga(a.Agent) {
			d.deliverToSuga(ctx, a, letters, now, st)
			continue
		}
		if WakeDue(a, letters, now, st.WakeIdle) && d.wakeAllowed(sid, now, st) {
			d.wake(ctx, a, letters, now)
		}
	}
}

// letterOrigin is the caller-scope ceiling owner of a letter's delivery: the caller that sent
// it (the stored FromCaller), or — for a letter an older row left unnamed — the owner of what
// sent it (a job's ceiling owner, a session's caller). "" means nobody could be named, which
// the relay refuses while the server restricts callers (sessionrelay/ceiling_origin.go).
func (d *Deliverer) letterOrigin(m jobstore.Mail) string {
	if c := strings.TrimSpace(m.FromCaller); c != "" {
		return c
	}
	switch m.FromKind {
	case jobstore.MailKindJob:
		if rec, ok, err := d.svc.store.GetJob(m.FromID); err == nil && ok {
			return strings.TrimSpace(rec.OriginCaller)
		}
	case jobstore.MailKindSession:
		if a, ok, err := d.svc.store.GetAgentSession(m.FromID); err == nil && ok {
			return strings.TrimSpace(a.CallerID)
		}
	}
	return ""
}

// WakeDue is the wake condition for a session with pending letters (设计「送达点」): the
// session is idle or offline (a running session injects at its next turn start anyway;
// waiting_reply waits for a person; ended / handed_off / needs_attention are not woken), and
// its last_seen_at is older than idle — except that an URGENT letter wakes an idle session
// at once. The per-session wake interval is the caller's (wakeAllowed).
func WakeDue(a jobstore.AgentSession, letters []jobstore.Mail, now time.Time, idle time.Duration) bool {
	if len(letters) == 0 {
		return false
	}
	switch a.State {
	case jobstore.SessionIdle:
		for _, m := range letters {
			if m.Urgent {
				return true
			}
		}
	case jobstore.SessionOffline:
	default:
		return false
	}
	return a.LastSeenAt <= 0 || now.Unix()-a.LastSeenAt >= int64(idle/time.Second)
}

// wakeAllowed applies the per-session wake interval and books the attempt: a wake that fails
// is not retried before the interval either, so a session nobody can reach costs one attempt
// per interval, not one per sweep.
func (d *Deliverer) wakeAllowed(sid string, now time.Time, st Settings) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.wokeAt[sid]; ok && now.Sub(last) < st.WakeInterval {
		return false
	}
	d.wokeAt[sid] = now
	return true
}

func (d *Deliverer) wake(ctx context.Context, a jobstore.AgentSession, letters []jobstore.Mail, now time.Time) {
	count := len(letters)
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	// The wake speaks under the deliverer's label, but the delivery belongs to whoever the
	// pending letter came from: a relay job it dispatches is bounded by that caller's ceiling.
	origin := ""
	if len(letters) > 0 {
		origin = d.letterOrigin(letters[0])
	}
	res, err := d.sender.Wake(sctx, a.SessionID, RenderWake(count), delivererBy, origin)
	if err != nil {
		slog.Info("mail deliverer: wake not delivered", "session", a.SessionID, "pending", count, "error", err)
		return
	}
	slog.Info("mail deliverer: woke session", "session", a.SessionID, "pending", count, "path", res.Path)
}

// renderFor renders a letter the way the hook does (the shared renderer), body cut to the
// per-letter budget.
func (d *Deliverer) renderFor(m jobstore.Mail) string {
	l := Letter{ID: m.ID, From: OriginOf(m).Display(), Urgent: m.Urgent, Body: m.Body}
	if utf8.RuneCountInString(l.Body) > deliverBodyRunes {
		l.Body, l.Truncated = string([]rune(l.Body)[:deliverBodyRunes]), true
	}
	if m.FromKind == jobstore.MailKindSession {
		if from, ok, err := d.svc.store.GetAgentSession(m.FromID); err == nil && ok {
			l.Topic = from.Topic
		}
	}
	return RenderLetter(l)
}

// deliverToSuga sends the letters of a suga session through `suga send`: claim → send →
// confirm. Exit 3 (not online) or any failure hands the letter back to pending at once and
// leaves the session alone for a while.
func (d *Deliverer) deliverToSuga(ctx context.Context, a jobstore.AgentSession, letters []jobstore.Mail, now time.Time, st Settings) {
	if a.State == jobstore.SessionEnded {
		return // kept until the deadline (a resumed session gets them)
	}
	d.mu.Lock()
	next := d.sugaNext[a.SessionID]
	d.mu.Unlock()
	if now.Before(next) {
		return
	}
	if len(letters) > deliverBatch {
		letters = letters[:deliverBatch]
	}
	store := d.svc.store
	for _, m := range letters {
		if ctx.Err() != nil {
			return
		}
		claimed, ok, err := store.ClaimMail(m.ID, jobstore.MailDelivererClaimer, st.Lease)
		if err != nil || !ok {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		_, err = d.sender.DeliverLetter(sctx, a.SessionID, d.renderFor(claimed), delivererBy, d.letterOrigin(claimed))
		cancel()
		if err != nil {
			reason := "suga send: " + err.Error()
			if sessionrelay.DeliverReason(err) == sessionrelay.ReasonNotRunning {
				reason = "session not online"
			}
			if _, _, rerr := store.ReleaseMailClaim(m.ID, jobstore.MailDelivererClaimer, reason); rerr != nil {
				slog.Warn("mail deliverer: release claim failed", "mail", m.ID, "error", rerr)
			}
			d.mu.Lock()
			d.sugaNext[a.SessionID] = now.Add(sugaRetryAfter)
			d.mu.Unlock()
			slog.Info("mail deliverer: suga letter left pending", "mail", m.ID, "session", a.SessionID, "reason", reason)
			return
		}
		if _, _, err := store.ConfirmMailClaim(m.ID, jobstore.MailDelivererClaimer); err != nil {
			slog.Warn("mail deliverer: confirm failed (the letter may be sent again)", "mail", m.ID, "error", err)
		}
	}
}

func (d *Deliverer) sweepJobs(ctx context.Context, now time.Time, st Settings) {
	ids, byID, err := d.pendingBy(jobstore.MailKindJob)
	if err != nil {
		slog.Warn("mail deliverer: list pending job mail failed", "error", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		rec, ok, err := d.svc.store.GetJob(id)
		if err != nil {
			continue
		}
		letters := byID[id]
		switch {
		case !ok:
			// The job row is gone (pruned, deleted): no later sweep can deliver these.
			d.holdJobLetters(letters, "收件 job 不存在")
		case rec.Interactive:
			d.holdJobLetters(letters, PtyHoldReason)
		case job.IsTerminal(rec.Status):
			// The recipient ended while the letter was waiting: park it and tell the sender
			// now instead of at the delivery deadline (gofer-0n6r).
			d.holdJobLetters(letters, "收件 job 已结束（"+rec.Status+"），这封信没人读了")
		case !d.svc.receivesJobMail(id):
			// A one-off run has no ACP session, so no turn of it can ever take a letter. New
			// sends are refused at the address (mail.Send); this catches letters stored
			// before that rule existed.
			d.holdJobLetters(letters, oneOffJobReason)
		case rec.Status == job.StatusAwaitingInput:
			d.sayToJob(ctx, id, letters[0], st)
		}
		// Anything else (queued / running session job) waits: a job that reaches
		// awaiting_input gets its letters at a later sweep.
	}
}

// oneOffJobReason explains a hold of a letter to a job that can never receive mail.
const oneOffJobReason = "收件是一次性作业（没有 ACP 会话），收不到信件"

// receivesJobMail reports whether a job can take a letter at all (见 SessionJobCheck): the
// deliverer's judgement when no seam is wired is the record's own state, i.e. "maybe".
func (s *Service) receivesJobMail(jobID string) bool {
	if s.sessionJob == nil {
		return true
	}
	session, found := s.sessionJob(jobID)
	return session || !found
}

// holdJobLetters parks the pending letters of one job and returns each to its sender.
func (d *Deliverer) holdJobLetters(letters []jobstore.Mail, reason string) {
	for _, m := range letters {
		if _, _, err := d.svc.HoldLetter(m.ID, reason); err != nil {
			slog.Warn("mail deliverer: holding a job letter failed", "mail", m.ID, "error", err)
		}
	}
}

// sayToJob delivers ONE letter to an ACP session job awaiting input (the job accepts one
// pending input at a time): claim → say → confirm.
func (d *Deliverer) sayToJob(ctx context.Context, jobID string, m jobstore.Mail, st Settings) {
	if d.jobs == nil {
		return
	}
	store := d.svc.store
	claimed, ok, err := store.ClaimMail(m.ID, jobstore.MailDelivererClaimer, st.Lease)
	if err != nil || !ok {
		return
	}
	if err := d.jobs.SaySession(jobID, d.renderFor(claimed)); err != nil {
		reason := "job say: " + err.Error()
		if _, _, rerr := store.ReleaseMailClaim(m.ID, jobstore.MailDelivererClaimer, reason); rerr != nil {
			slog.Warn("mail deliverer: release claim failed", "mail", m.ID, "error", rerr)
		}
		if !errors.Is(err, job.ErrJobNotRunning) && !errors.Is(err, job.ErrInvalidRequest) {
			slog.Info("mail deliverer: job say failed", "mail", m.ID, "job", jobID, "error", err)
		}
		return
	}
	if _, _, err := store.ConfirmMailClaim(m.ID, jobstore.MailDelivererClaimer); err != nil {
		slog.Warn("mail deliverer: confirm failed (the letter may be sent again)", "mail", m.ID, "error", err)
	}
}
