package workerenroll

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// Audit event kinds recorded through AuditSink.
const (
	AuditRequest        = "worker.enroll.request"
	AuditAccept         = "worker.enroll.accept"
	AuditReject         = "worker.enroll.reject"
	AuditExpire         = "worker.enroll.expire"
	AuditCollect        = "worker.enroll.collect"
	AuditCancel         = "worker.enroll.cancel"
	AuditCodeMismatch   = "worker.enroll.code_mismatch"
	AuditSecretMismatch = "worker.enroll.secret_mismatch"
)

// Default numbers (design 「默认数值」); a zero Settings field falls back to these.
const (
	DefaultDecideSec  = 120
	DefaultCollectSec = 60
	DefaultMaxPending = 20
	// MaxCodeFailures and MaxSecretFailures void a request on the Nth wrong try.
	MaxCodeFailures   = 5
	MaxSecretFailures = 5

	pairingCodeLen = 8
	// pairingAlphabet is 30 characters: A-Z without O I L U, plus 2-9.
	pairingAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	systemActor     = "system"
)

// Sentinel errors. Callers (HTTP handlers) map them to responses; ErrNotFound covers
// both "no such request" and "wrong secret" so the two are indistinguishable.
var (
	ErrDisabled       = errors.New("workerenroll: worker enrollment is disabled")
	ErrNotFound       = errors.New("workerenroll: enrollment not found")
	ErrCapReached     = errors.New("workerenroll: too many pending enrollments")
	ErrNotPending     = errors.New("workerenroll: enrollment is no longer pending")
	ErrWrongState     = errors.New("workerenroll: enrollment is in the wrong state for this action")
	ErrCodeMismatch   = errors.New("workerenroll: pairing code mismatch")
	ErrTokenMismatch  = errors.New("workerenroll: configured worker token does not match the issued one")
	ErrInvalidRequest = errors.New("workerenroll: invalid request")
	// ErrRateLimited and ErrSourceBlocked are what a Limiter returns.
	ErrRateLimited   = errors.New("workerenroll: rate limited")
	ErrSourceBlocked = errors.New("workerenroll: source is blocked")
)

// Settings is the slice of server.worker_enrollment this package reads on each call.
type Settings struct {
	Enabled    bool
	DecideSec  int
	CollectSec int
	MaxPending int
}

func (s Settings) decide() int64 {
	if s.DecideSec <= 0 {
		return DefaultDecideSec
	}
	return int64(s.DecideSec)
}

func (s Settings) collect() int64 {
	if s.CollectSec <= 0 {
		return DefaultCollectSec
	}
	return int64(s.CollectSec)
}

func (s Settings) maxPending() int {
	if s.MaxPending <= 0 {
		return DefaultMaxPending
	}
	return s.MaxPending
}

// ConfigSource reads the current config snapshot on every call (never cached), so
// hot-reloads apply immediately.
type ConfigSource interface {
	// Enrollment returns the enrollment settings.
	Enrollment() Settings
	// WorkerToken returns the token configured for worker id in server.workers.
	WorkerToken(id string) (token string, ok bool)
}

// Registrar writes and revokes worker registrations; serve implements it with
// core.Update + config.RegisterWorkerInto / RemoveWorkerFrom + the hub.
type Registrar interface {
	Register(id string, labels, projects []string, token string) error
	// Remove revokes worker id only if its configured token still has fingerprint fp.
	Remove(id, fp string) (bool, error)
	Disconnect(id string)
}

// AuditSink records an audit event; target is the enrollment id.
type AuditSink interface {
	Record(kind, enrollID, actor string)
}

// Limiter is the per-source request limiter (E2b). nil means unlimited. It returns
// ErrRateLimited / ErrSourceBlocked (or any error) to refuse a request.
type Limiter interface {
	Allow(source string, now time.Time) error
}

// SourceBlocker receives "block this source" decisions made on reject (E2b's block table).
type SourceBlocker interface {
	Block(source string, d time.Duration)
}

// Options builds a Service. Store, Registrar, Audit and Config are required.
type Options struct {
	Store     *jobstore.Store
	Registrar Registrar
	Audit     AuditSink
	Config    ConfigSource
	Limiter   Limiter       // optional
	Blocker   SourceBlocker // optional
	Clock     func() time.Time
}

// Service is the enrollment state machine.
type Service struct {
	st      *jobstore.Store
	reg     Registrar
	audit   AuditSink
	cfg     ConfigSource
	limiter Limiter
	blocker SourceBlocker
	clock   func() time.Time
}

// New validates opts and returns a Service.
func New(o Options) (*Service, error) {
	if o.Store == nil || o.Registrar == nil || o.Audit == nil || o.Config == nil {
		return nil, errors.New("workerenroll: Store, Registrar, Audit and Config are required")
	}
	clock := o.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{st: o.Store, reg: o.Registrar, audit: o.Audit, cfg: o.Config,
		limiter: o.Limiter, blocker: o.Blocker, clock: clock}, nil
}

func (s *Service) now() int64 { return s.clock().Unix() }

// RequestInput is a validated anonymous enrollment request (field limits are
// enforced by the HTTP entry).
type RequestInput struct {
	RequestedID string
	Labels      []string
	HostJSON    string
	SecretHash  string // SHA-256 hex of the enroll secret, chosen by the worker
	Source      string
}

// RequestResult is what the applicant sees.
type RequestResult struct {
	EnrollID    string
	PairingCode string
	DecideUntil int64
}

// Request creates a pending enrollment. It supersedes the same source's older pending
// requests and fails with ErrCapReached at the global pending cap.
func (s *Service) Request(_ context.Context, in RequestInput) (RequestResult, error) {
	set := s.cfg.Enrollment()
	if !set.Enabled {
		return RequestResult{}, ErrDisabled
	}
	hash := strings.ToLower(strings.TrimSpace(in.SecretHash))
	if !isHex64(hash) {
		return RequestResult{}, fmt.Errorf("%w: secret_hash must be 64 hex characters", ErrInvalidRequest)
	}
	if s.limiter != nil {
		if err := s.limiter.Allow(in.Source, s.clock()); err != nil {
			return RequestResult{}, err
		}
	}
	id, err := newEnrollID()
	if err != nil {
		return RequestResult{}, err
	}
	code, err := NewPairingCode()
	if err != nil {
		return RequestResult{}, err
	}
	labels, _ := json.Marshal(nonNil(in.Labels))
	now := s.now()
	e := jobstore.WorkerEnrollment{
		ID: id, SecretHash: hash, PairingCode: code, RequestedID: in.RequestedID,
		LabelsJSON: string(labels), HostJSON: in.HostJSON, Source: in.Source,
		CreatedAt: now, DecideUntil: now + set.decide(),
	}
	if _, err := s.st.InsertWorkerEnrollment(e, set.maxPending()); err != nil {
		if errors.Is(err, jobstore.ErrWorkerEnrollmentCap) {
			return RequestResult{}, ErrCapReached
		}
		return RequestResult{}, err
	}
	s.audit.Record(AuditRequest, id, in.Source)
	return RequestResult{EnrollID: id, PairingCode: code, DecideUntil: e.DecideUntil}, nil
}

// PollResult is the applicant's view of a request. Token is set only for an accepted
// request whose configured token still matches the issued fingerprint.
type PollResult struct {
	State        string
	WorkerID     string
	Token        string
	CollectUntil int64
	Reason       string
}

// Poll authenticates the applicant with the raw enroll secret and returns the current
// state. While accepted and inside the collect window it can be called repeatedly and
// returns the token each time (the response may have been lost); the token is read
// from the current config and refused (ErrTokenMismatch, no token) if its fingerprint
// differs from the one issued. A wrong secret or unknown id is ErrNotFound; the 5th
// wrong secret voids the request.
func (s *Service) Poll(_ context.Context, id, secret string) (PollResult, error) {
	e, err := s.authenticate(id, secret)
	if err != nil {
		return PollResult{}, err
	}
	switch e.State {
	case jobstore.EnrollPending:
		if s.now() >= e.DecideUntil {
			if _, err := s.expireRow(e); err != nil {
				return PollResult{}, err
			}
			return PollResult{State: jobstore.EnrollExpired}, nil
		}
		return PollResult{State: jobstore.EnrollPending}, nil
	case jobstore.EnrollAccepting:
		// Still being registered; the applicant keeps waiting.
		return PollResult{State: jobstore.EnrollPending}, nil
	case jobstore.EnrollAccepted:
		if s.now() >= e.CollectUntil {
			if _, err := s.expireRow(e); err != nil {
				return PollResult{}, err
			}
			return PollResult{State: jobstore.EnrollExpired}, nil
		}
		token, ok := s.cfg.WorkerToken(e.WorkerID)
		if !ok || !fpEqual(config.WorkerTokenFingerprint(token), e.TokenFP) {
			return PollResult{State: jobstore.EnrollAccepted, WorkerID: e.WorkerID, CollectUntil: e.CollectUntil}, ErrTokenMismatch
		}
		return PollResult{State: jobstore.EnrollAccepted, WorkerID: e.WorkerID, Token: token, CollectUntil: e.CollectUntil}, nil
	default:
		return PollResult{State: e.State, Reason: e.Reason}, nil
	}
}

// Confirm is the applicant's "token written" acknowledgement: accepted -> collected.
// It is idempotent for an already collected request. Confirm is deliberately lenient
// about the collect deadline: as long as the sweep has not revoked the registration
// the row is still accepted, and a worker that is slow to finish (doctor) must not
// lose its registration to a late confirm; the conditional update decides the race.
func (s *Service) Confirm(_ context.Context, id, secret string) error {
	e, err := s.authenticate(id, secret)
	if err != nil {
		return err
	}
	switch e.State {
	case jobstore.EnrollCollected:
		return nil
	case jobstore.EnrollAccepted:
		ok, err := s.st.TransitionWorkerEnrollment(id, []string{jobstore.EnrollAccepted}, jobstore.EnrollCollected, s.now(), "", "")
		if err != nil {
			return err
		}
		if !ok {
			return ErrWrongState
		}
		s.audit.Record(AuditCollect, id, e.Source)
		return nil
	default:
		return ErrWrongState
	}
}

// Cancel is the applicant giving up: pending -> cancelled; accepted-but-uncollected is
// expired and its registration revoked (fingerprint-checked). Other states are a no-op.
func (s *Service) Cancel(_ context.Context, id, secret string) error {
	e, err := s.authenticate(id, secret)
	if err != nil {
		return err
	}
	switch e.State {
	case jobstore.EnrollPending:
		ok, err := s.st.TransitionWorkerEnrollment(id, []string{jobstore.EnrollPending}, jobstore.EnrollCancelled, s.now(), "", "cancelled by applicant")
		if err != nil {
			return err
		}
		if ok {
			s.audit.Record(AuditCancel, id, e.Source)
		}
	case jobstore.EnrollAccepted:
		if _, err := s.revokeAndExpire(e, "cancelled by applicant"); err != nil {
			return err
		}
		s.audit.Record(AuditCancel, id, e.Source)
	}
	return nil
}

// AcceptInput is the administrator's decision.
type AcceptInput struct {
	PairingCode string
	WorkerID    string
	Labels      []string
	Projects    []string
	Actor       string
}

// AcceptResult never carries the token.
type AcceptResult struct {
	WorkerID     string
	CollectUntil int64
}

// Accept checks the pairing code, claims the request (conditional pending -> accepting
// guarded by the decision deadline), registers the worker, and moves it to accepted
// with a collect deadline. A failed registration puts the request back to pending and
// returns the error unchanged. A lost claim is ErrNotPending.
func (s *Service) Accept(_ context.Context, id string, in AcceptInput) (AcceptResult, error) {
	e, err := s.st.GetWorkerEnrollment(id)
	if err != nil {
		return AcceptResult{}, mapNotFound(err)
	}
	if e.State != jobstore.EnrollPending {
		return AcceptResult{}, ErrNotPending
	}
	if s.now() >= e.DecideUntil {
		if _, err := s.expireRow(e); err != nil {
			return AcceptResult{}, err
		}
		return AcceptResult{}, ErrNotPending
	}
	if !codeEqual(in.PairingCode, e.PairingCode) {
		n, err := s.st.BumpWorkerEnrollmentCodeFailures(id)
		if err != nil {
			return AcceptResult{}, err
		}
		s.audit.Record(AuditCodeMismatch, id, in.Actor)
		if n >= MaxCodeFailures {
			if _, err := s.expireRow(e); err != nil {
				return AcceptResult{}, err
			}
		}
		return AcceptResult{}, ErrCodeMismatch
	}
	token, err := config.NewWorkerToken()
	if err != nil {
		return AcceptResult{}, err
	}
	fp := config.WorkerTokenFingerprint(token)
	labels, _ := json.Marshal(nonNil(in.Labels))
	projects, _ := json.Marshal(nonNil(in.Projects))
	ok, err := s.st.ClaimWorkerEnrollment(jobstore.WorkerEnrollmentClaim{
		ID: id, Now: s.now(), WorkerID: in.WorkerID, LabelsJSON: string(labels),
		ProjectsJSON: string(projects), TokenFP: fp, DecidedBy: in.Actor,
	})
	if err != nil {
		return AcceptResult{}, err
	}
	if !ok {
		return AcceptResult{}, ErrNotPending
	}
	if err := s.reg.Register(in.WorkerID, nonNil(in.Labels), nonNil(in.Projects), token); err != nil {
		if _, rerr := s.st.RevertWorkerEnrollmentAccept(id); rerr != nil {
			return AcceptResult{}, errors.Join(err, rerr)
		}
		return AcceptResult{}, err
	}
	collectUntil := s.now() + s.cfg.Enrollment().collect()
	ok, err = s.st.FinishWorkerEnrollmentAccept(id, collectUntil)
	if err != nil || !ok {
		// Registered but the row moved under us (reconcile); do not leave a worker nobody holds.
		if removed, _ := s.reg.Remove(in.WorkerID, fp); removed {
			s.reg.Disconnect(in.WorkerID)
		}
		if err != nil {
			return AcceptResult{}, err
		}
		return AcceptResult{}, ErrNotPending
	}
	s.audit.Record(AuditAccept, id, in.Actor)
	return AcceptResult{WorkerID: in.WorkerID, CollectUntil: collectUntil}, nil
}

// RejectInput is the administrator's rejection.
type RejectInput struct {
	Reason string
	Actor  string
	// BlockSourceMin > 0 asks the SourceBlocker to refuse the source for that long.
	BlockSourceMin int
}

// Reject moves a pending request to rejected and optionally blocks its source.
func (s *Service) Reject(_ context.Context, id string, in RejectInput) error {
	e, err := s.st.GetWorkerEnrollment(id)
	if err != nil {
		return mapNotFound(err)
	}
	if e.State != jobstore.EnrollPending {
		return ErrNotPending
	}
	if s.now() >= e.DecideUntil {
		if _, err := s.expireRow(e); err != nil {
			return err
		}
		return ErrNotPending
	}
	ok, err := s.st.TransitionWorkerEnrollment(id, []string{jobstore.EnrollPending}, jobstore.EnrollRejected, s.now(), in.Actor, in.Reason)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotPending
	}
	s.audit.Record(AuditReject, id, in.Actor)
	if in.BlockSourceMin > 0 && s.blocker != nil && e.Source != "" {
		s.blocker.Block(e.Source, time.Duration(in.BlockSourceMin)*time.Minute)
	}
	return nil
}

// Get returns one row (admin / sweep use); callers must not expose SecretHash / TokenFP.
func (s *Service) Get(id string) (jobstore.WorkerEnrollment, error) {
	e, err := s.st.GetWorkerEnrollment(id)
	return e, mapNotFound(err)
}

// ListAdmin returns the open requests plus those created within the last `recent`.
func (s *Service) ListAdmin(recent time.Duration) ([]jobstore.WorkerEnrollment, error) {
	return s.st.ListWorkerEnrollmentsAdmin(s.now() - int64(recent.Seconds()))
}

// Settings returns the current enrollment settings.
func (s *Service) Settings() Settings { return s.cfg.Enrollment() }

// Expire is the sweep seam: it expires one request by id if its deadline has passed
// (pending past decide_until, accepted past collect_until; the latter also revokes the
// registration when the fingerprint still matches). accepting rows are left to the
// reconcile. It reports whether this call expired the request.
func (s *Service) Expire(id string) (bool, error) {
	e, err := s.st.GetWorkerEnrollment(id)
	if err != nil {
		return false, mapNotFound(err)
	}
	now := s.now()
	switch {
	case e.State == jobstore.EnrollPending && now >= e.DecideUntil,
		e.State == jobstore.EnrollAccepted && now >= e.CollectUntil:
		return s.expireRow(e)
	}
	return false, nil
}

// MarkCollectedByWorker is the hub first-connect seam: if worker id has an accepted
// request and the configured token still has the issued fingerprint, it is collected.
func (s *Service) MarkCollectedByWorker(workerID string) (bool, error) {
	e, err := s.st.FindAcceptedWorkerEnrollment(workerID)
	if err != nil {
		if errors.Is(err, jobstore.ErrWorkerEnrollmentNotFound) {
			return false, nil
		}
		return false, err
	}
	token, ok := s.cfg.WorkerToken(workerID)
	if !ok || !fpEqual(config.WorkerTokenFingerprint(token), e.TokenFP) {
		return false, nil
	}
	won, err := s.st.TransitionWorkerEnrollment(e.ID, []string{jobstore.EnrollAccepted}, jobstore.EnrollCollected, s.now(), "", "")
	if err == nil && won {
		s.audit.Record(AuditCollect, e.ID, e.Source)
	}
	return won, err
}

// authenticate loads the row and checks the raw enroll secret in constant time. Unknown
// id and wrong secret both give ErrNotFound; the 5th wrong secret voids the request.
func (s *Service) authenticate(id, secret string) (jobstore.WorkerEnrollment, error) {
	e, err := s.st.GetWorkerEnrollment(id)
	if err != nil {
		return e, mapNotFound(err)
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(e.SecretHash)) == 1 {
		return e, nil
	}
	n, err := s.st.BumpWorkerEnrollmentSecretFailures(id)
	if err != nil {
		return e, mapNotFound(err)
	}
	s.audit.Record(AuditSecretMismatch, id, e.Source)
	if n >= MaxSecretFailures {
		if e.State == jobstore.EnrollPending || e.State == jobstore.EnrollAccepted {
			_, _ = s.expireRow(e)
		}
	}
	return e, ErrNotFound
}

// expireRow expires e from pending, or from accepted (revoking the registration).
func (s *Service) expireRow(e jobstore.WorkerEnrollment) (bool, error) {
	if e.State == jobstore.EnrollAccepted {
		return s.revokeAndExpire(e, "collect window elapsed")
	}
	ok, err := s.st.TransitionWorkerEnrollment(e.ID, []string{jobstore.EnrollPending}, jobstore.EnrollExpired, s.now(), systemActor, "")
	if err == nil && ok {
		s.audit.Record(AuditExpire, e.ID, systemActor)
	}
	return ok, err
}

// revokeAndExpire wins the accepted -> expired transition first, then revokes the
// registration (Remove checks the token fingerprint, so a worker that was re-added
// under the same id is left alone) and disconnects it only if something was removed.
func (s *Service) revokeAndExpire(e jobstore.WorkerEnrollment, reason string) (bool, error) {
	ok, err := s.st.TransitionWorkerEnrollment(e.ID, []string{jobstore.EnrollAccepted}, jobstore.EnrollExpired, s.now(), systemActor, reason)
	if err != nil || !ok {
		return false, err
	}
	removed, rerr := s.reg.Remove(e.WorkerID, e.TokenFP)
	if removed {
		s.reg.Disconnect(e.WorkerID)
	}
	s.audit.Record(AuditExpire, e.ID, systemActor)
	return true, rerr
}

// NewPairingCode returns a random 8-character code from the 30-character alphabet.
func NewPairingCode() (string, error) {
	out := make([]byte, 0, pairingCodeLen)
	buf := make([]byte, 16)
	for len(out) < pairingCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			// 240 = 30*8: reject the tail to keep the distribution uniform.
			if b >= 240 {
				continue
			}
			out = append(out, pairingAlphabet[int(b)%len(pairingAlphabet)])
			if len(out) == pairingCodeLen {
				break
			}
		}
	}
	return string(out), nil
}

// NormalizePairingCode upper-cases and strips spaces and dashes (the display form is 4+4).
func NormalizePairingCode(s string) string {
	s = strings.ToUpper(s)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, s)
}

func codeEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(NormalizePairingCode(got)), []byte(want)) == 1
}

func fpEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func newEnrollID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "we_" + hex.EncodeToString(b), nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mapNotFound(err error) error {
	if errors.Is(err, jobstore.ErrWorkerEnrollmentNotFound) {
		return ErrNotFound
	}
	return err
}
