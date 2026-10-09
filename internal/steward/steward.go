// Package steward is the W2b steward (design 2026-10-05 §9 / §14.4): a resident ACP session
// that only schedules and tidies the work items — it reads, notes, reminds, suggests merges
// and asks sessions to report, but never decides (no done / dropped, no job submit, no
// config). Everything it needs lives in the database (work items, journal, request ledger,
// the versioned steward notes), so the session is disposable: it ends when idle, and a new
// one — on any agent — is rebuilt from the prime.
//
// The package owns the lifecycle, the prime, the notes, the daily review and the event
// inbox. It reaches the job layer only through SessionHost (G022: httpapi adapts
// job.Service), and the credential that actually bounds the steward is enforced by the
// HTTP layer, not here.
package steward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// Errors surfaced to the entry layers.
var (
	ErrDisabled = errors.New("steward is not enabled (set steward.enabled in the settings)")
	ErrNoAgent  = errors.New("no steward agent chosen (set steward.agent to an installed acp-agent)")
	ErrBusy     = errors.New("steward is busy with another turn; try again shortly")
	ErrEmpty    = errors.New("empty message")
)

// StartSpec describes the steward's session job.
type StartSpec struct {
	Agent   string
	Project string
	Title   string
	// Prompt is the first turn: the prime (plus the first question or task).
	Prompt string
	// IdleSec ends the session after that long without a turn.
	IdleSec int
}

// JobInfo is what the steward needs to know about its session job.
type JobInfo struct {
	ID            string
	Agent         string
	Status        string
	Live          bool // not terminal
	AwaitingInput bool // between turns: a message can be sent
	TurnNo        int
	StartedAt     int64
}

// SessionHost is the seam to the job layer (httpapi adapts job.Service).
type SessionHost interface {
	// StartSession submits the steward's resident session job and returns its id.
	StartSession(StartSpec) (string, error)
	// Say sends the next turn to the live session.
	Say(jobID, text string) error
	// End asks the session to end after the current turn.
	End(jobID string) error
	Job(jobID string) (JobInfo, bool)
	// CheckAgent says why the agent cannot run as steward ("" nil = usable).
	CheckAgent(agent string) error
}

// Session states shown in the status.
const (
	StateStopped = "not_started"
	StateRunning = "running"
	StateIdle    = "idle"
)

// work_kv keys (the steward keeps nothing else outside the database tables).
const (
	kvJobID         = "steward.job_id"
	kvAgent         = "steward.agent"
	kvStartedAt     = "steward.started_at"
	kvLastActive    = "steward.last_active_at"
	kvLastReviewDay = "steward.last_review_day"
	kvReviewCutoff  = "steward.review_cutoff"
	kvLastEventWake = "steward.last_event_wake_at"
)

const (
	defaultAskWait       = 90 * time.Second
	defaultPollEvery     = 300 * time.Millisecond
	defaultReviewTimeout = 20 * time.Minute
	tickEvery            = 30 * time.Second
	// reviewWindow keeps a server that comes up late in the day from running a stale
	// "morning" review hours after it was due.
	reviewWindow = 6 * time.Hour
)

// Service is the steward.
type Service struct {
	store *jobstore.Store
	work  *work.Service
	host  SessionHost

	cfgFn func() (config.StewardConfig, config.WorkConfig)
	nowFn func() time.Time

	// mu serialises lifecycle changes (start / stop / reconcile); askMu serialises asks
	// (an ask may wait for the previous turn to end); tickMu serialises Tick.
	mu       sync.Mutex
	askMu    sync.Mutex
	tickMu   sync.Mutex
	reviewMu sync.Mutex
	reviewOn bool

	askWait       time.Duration
	pollEvery     time.Duration
	reviewTimeout time.Duration
	primeMax      int
	// todayUnadvised lists the 「今天」 cards without advice (N3 T4, today.go).
	todayUnadvised func() ([]string, error)

	bg sync.WaitGroup
}

// New builds the steward over the shared stores and the session host.
func New(store *jobstore.Store, ws *work.Service, host SessionHost) *Service {
	return &Service{
		store: store, work: ws, host: host, nowFn: time.Now,
		askWait: defaultAskWait, pollEvery: defaultPollEvery, reviewTimeout: defaultReviewTimeout,
		primeMax: config.StewardPrimeMaxBytes,
	}
}

// SetConfigFn supplies the live steward: and work: blocks (read on every use, so a hot
// reload applies to the next one).
func (s *Service) SetConfigFn(fn func() (config.StewardConfig, config.WorkConfig)) { s.cfgFn = fn }

// SetNow overrides the clock (tests).
func (s *Service) SetNow(fn func() time.Time) {
	if fn != nil {
		s.nowFn = fn
	}
}

// WaitIdle blocks until the background work (review completion watchers) finished (tests).
func (s *Service) WaitIdle() { s.bg.Wait() }

func (s *Service) cfg() (config.StewardConfig, config.WorkConfig) {
	if s.cfgFn != nil {
		return s.cfgFn()
	}
	return config.StewardConfig{}, config.WorkConfig{}
}

func (s *Service) kv(k string) string {
	v, _ := s.store.GetWorkKV(k)
	return v
}

func (s *Service) kvInt(k string) int64 {
	n, _ := strconv.ParseInt(s.kv(k), 10, 64)
	return n
}

func (s *Service) setKV(k, v string) {
	if err := s.store.SetWorkKV(k, v); err != nil {
		slog.Warn("steward.kv_failed", "event", "steward.kv_failed", "key", k, "err", err)
	}
}

// By is the speaker label of the steward for the given agent (steward(<agent>)).
func (s *Service) By() string {
	c, _ := s.cfg()
	if a := c.AgentName(); a != "" {
		return work.StewardBy(a)
	}
	return work.ActorSteward
}

// ready checks the configuration allows a session to run.
func (s *Service) ready() (config.StewardConfig, error) {
	c, _ := s.cfg()
	if !c.Enabled {
		return c, ErrDisabled
	}
	if c.AgentName() == "" {
		return c, ErrNoAgent
	}
	return c, nil
}

// ---------------------------------------------------------------- status

// Status is the steward's state for the settings page, the panel and the CLI.
type Status struct {
	Enabled bool   `json:"enabled"`
	Agent   string `json:"agent"`
	Project string `json:"project"`
	// State is not_started | running (a turn in progress) | idle (waiting for input).
	State string `json:"state"`
	JobID string `json:"job_id,omitempty"`
	// JobAgent is the agent of the live session (differs from Agent right after a switch,
	// until the next reconcile ends it).
	JobAgent     string `json:"job_agent,omitempty"`
	StartedAt    int64  `json:"started_at,omitempty"`
	LastActiveAt int64  `json:"last_active_at,omitempty"`
	TurnNo       int    `json:"turn_no,omitempty"`
	IdleEndMin   int    `json:"idle_end_min"`
	ReviewTime   string `json:"review_time"`
	EventWake    bool   `json:"event_wake"`
	// AgentError says why the chosen agent cannot run ("" = usable or none chosen).
	AgentError    string                  `json:"agent_error,omitempty"`
	NotesVersion  int                     `json:"notes_version"`
	NotesBytes    int                     `json:"notes_bytes"`
	NotesNeedSlim bool                    `json:"notes_need_slim"`
	PendingEvents int                     `json:"pending_events"`
	LastReview    *jobstore.StewardReview `json:"last_review,omitempty"`
}

// Status reports the current state. It reads the recorded session job and asks the host
// whether it still lives (a session that ended by itself reads as not_started).
func (s *Service) Status() Status {
	c, w := s.cfg()
	h, m := c.ReviewClock(w)
	st := Status{
		Enabled: c.Enabled, Agent: c.AgentName(), Project: c.ProjectKey(), State: StateStopped,
		IdleEndMin: int(c.IdleEnd().Minutes()), ReviewTime: fmt.Sprintf("%02d:%02d", h, m), EventWake: c.EventWake,
	}
	if st.Agent != "" && s.host != nil {
		if err := s.host.CheckAgent(st.Agent); err != nil {
			st.AgentError = err.Error()
		}
	}
	if info, ok := s.liveJob(); ok {
		st.JobID, st.JobAgent, st.StartedAt, st.TurnNo = info.ID, info.Agent, info.StartedAt, info.TurnNo
		st.LastActiveAt = s.kvInt(kvLastActive)
		if info.AwaitingInput {
			st.State = StateIdle
		} else {
			st.State = StateRunning
		}
	}
	if n, err := s.NotesStatus(); err == nil {
		st.NotesVersion, st.NotesBytes, st.NotesNeedSlim = n.Version, n.Bytes, n.NeedSlim
	}
	if ev, err := s.store.PendingStewardEvents(500); err == nil {
		st.PendingEvents = len(ev)
	}
	if r, ok, err := s.store.LastStewardReview(); err == nil && ok {
		st.LastReview = &r
	}
	return st
}

// liveJob returns the recorded steward session job when it still lives; a recorded id
// whose job ended is forgotten.
func (s *Service) liveJob() (JobInfo, bool) {
	id := s.kv(kvJobID)
	if id == "" || s.host == nil {
		return JobInfo{}, false
	}
	info, ok := s.host.Job(id)
	if !ok || !info.Live {
		s.forget()
		return JobInfo{}, false
	}
	return info, true
}

func (s *Service) forget() {
	for _, k := range []string{kvJobID, kvAgent, kvStartedAt} {
		if s.kv(k) != "" {
			s.setKV(k, "")
		}
	}
}

// ---------------------------------------------------------------- lifecycle

// StartResult is what a start did.
type StartResult struct {
	JobID string `json:"job_id"`
	// Started is true when this call opened a new session (false = it was already live).
	Started bool `json:"started"`
}

// readyLine is the first-turn instruction when nobody asked anything yet.
const readyLine = "读完以上内容后，只回复一行：管家已就绪（说明未结工作项数量）。不要调用任何工具。"

// ensureSession makes sure a session on the configured agent lives, opening one from the
// prime when needed; firstTurn is what the new session's first turn asks (the prime is
// always in front). It reports whether a session was opened. Caller holds s.mu.
func (s *Service) ensureSession(firstTurn string) (jobID string, started bool, err error) {
	c, err := s.ready()
	if err != nil {
		return "", false, err
	}
	if info, ok := s.liveJob(); ok {
		if info.Agent == c.AgentName() {
			return info.ID, false, nil
		}
		// The agent was switched: the old session is disposable, end it and rebuild.
		slog.Info("steward.agent_switched", "event", "steward.agent_switched", "from", info.Agent, "to", c.AgentName())
		_ = s.host.End(info.ID)
		s.forget()
	}
	if err := s.host.CheckAgent(c.AgentName()); err != nil {
		return "", false, fmt.Errorf("steward agent %q cannot run: %w", c.AgentName(), err)
	}
	prime, err := s.BuildPrime(s.nowFn())
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(firstTurn) == "" {
		firstTurn = readyLine
	}
	id, err := s.host.StartSession(StartSpec{
		Agent: c.AgentName(), Project: c.ProjectKey(), Title: "管家 · " + c.AgentName(),
		Prompt: prime.Text + "\n\n---\n\n" + firstTurn, IdleSec: int(c.IdleEnd().Seconds()),
	})
	if err != nil {
		return "", false, err
	}
	now := strconv.FormatInt(s.nowFn().Unix(), 10)
	s.setKV(kvJobID, id)
	s.setKV(kvAgent, c.AgentName())
	s.setKV(kvStartedAt, now)
	s.setKV(kvLastActive, now)
	return id, true, nil
}

// Start opens the steward session when none lives (a no-op otherwise).
func (s *Service) Start(ctx context.Context) (StartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, started, err := s.ensureSession("")
	return StartResult{JobID: id, Started: started}, err
}

// Stop ends the live session (the next need rebuilds it from the prime). It reports
// whether there was one.
func (s *Service) Stop() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.liveJob()
	if !ok {
		return false, nil
	}
	err := s.host.End(info.ID)
	s.forget()
	return true, err
}

// Restart ends the live session and opens a fresh one from the prime.
func (s *Service) Restart(ctx context.Context) (StartResult, error) {
	if _, err := s.ready(); err != nil {
		return StartResult{}, err
	}
	if _, err := s.Stop(); err != nil {
		slog.Debug("steward.stop_failed", "event", "steward.stop_failed", "err", err)
	}
	return s.Start(ctx)
}

var errSessionGone = errors.New("steward session already gone")

// Reconcile ends a session that no longer matches the configuration: the steward was
// switched off, or its agent was switched (the next need rebuilds on the new agent).
func (s *Service) Reconcile() {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.liveJob()
	if !ok {
		return
	}
	c, _ := s.cfg()
	if c.Enabled && info.Agent == c.AgentName() {
		return
	}
	reason := "agent_switched"
	if !c.Enabled {
		reason = "disabled"
	}
	slog.Info("steward.session_ended", "event", "steward.session_ended", "job", info.ID, "reason", reason, "agent", info.Agent)
	_ = s.host.End(info.ID)
	s.forget()
}

// ---------------------------------------------------------------- ask

// AskResult is where the answer will appear.
type AskResult struct {
	JobID string `json:"job_id"`
	// Started is true when the question opened a new session (its prime rode along).
	Started bool `json:"started"`
}

// Ask puts a question to the steward: an unstarted steward is started with the question as
// its first turn; a live one is waited for until it is between turns, then spoken to. The
// answer streams through the session job's ACP events (the panel follows JobID).
func (s *Service) Ask(ctx context.Context, text, by string) (AskResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return AskResult{}, ErrEmpty
	}
	s.askMu.Lock()
	defer s.askMu.Unlock()
	turn := wrapQuestion(text, by)

	s.mu.Lock()
	id, started, err := s.ensureSession(turn)
	s.mu.Unlock()
	if err != nil {
		return AskResult{}, err
	}
	if started {
		return AskResult{JobID: id, Started: true}, nil
	}
	if err := s.say(ctx, id, turn); err != nil {
		if !errors.Is(err, errSessionGone) {
			return AskResult{}, err
		}
		// The session ended between the check and the send (idle end): rebuild it.
		s.mu.Lock()
		id, started, err = s.ensureSession(turn)
		s.mu.Unlock()
		if err != nil {
			return AskResult{}, err
		}
		return AskResult{JobID: id, Started: started}, nil
	}
	return AskResult{JobID: id}, nil
}

// say waits until the session is between turns (bounded) and sends the turn.
func (s *Service) say(ctx context.Context, jobID, text string) error {
	deadline := time.Now().Add(s.askWait)
	for {
		info, ok := s.host.Job(jobID)
		if !ok || !info.Live {
			s.forget()
			return errSessionGone
		}
		if info.AwaitingInput {
			err := s.host.Say(jobID, text)
			if err == nil {
				s.setKV(kvLastActive, strconv.FormatInt(s.nowFn().Unix(), 10))
				return nil
			}
			// Lost a race with another sender: fall through and wait again.
			if time.Now().After(deadline) {
				return err
			}
		}
		if time.Now().After(deadline) {
			return ErrBusy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollEvery):
		}
	}
}

func wrapQuestion(text, by string) string {
	by = strings.TrimSpace(by)
	if by == "" {
		by = "human"
	}
	return "## 来自 " + by + " 的提问\n\n" + text + "\n\n（先用 gofer_work_list / gofer_work_get 核对最新数据再回答；中文，简洁。）"
}
