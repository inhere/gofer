// Package webpush owns browser Web Push encryption, VAPID identity, subscriptions,
// one-time notification actions and asynchronous event dispatch.
package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

const eventQueueSize = 128

var ErrNoSubscriptions = errors.New("webpush: caller has no subscriptions")

// ErrActionForbidden: the notification's caller may not answer an interaction right now —
// it does not hold the answer permission (design §6「动作按钮在签发和兑现时都检查权限」).
// Minting a button and redeeming its token both answer it.
var ErrActionForbidden = errors.New("webpush: caller may not act on an interaction")

// Jobs is the authoritative interaction/job seam used by dispatch and actions.
type Jobs interface {
	Get(id string) (job.JobResult, bool)
	GetInteractions(jobID string) ([]job.Interaction, error)
	AnswerInteractionByPush(jobID, interactionID, answer string) (job.Interaction, error)
}

// Options contains assembly-owned dependencies. Time/random/HTTP are injectable
// boundaries; production leaves them nil for secure defaults.
type Options struct {
	Store       *jobstore.Store
	Jobs        Jobs
	ConfigDir   string
	Subject     string
	HTTPClient  *http.Client
	Now         func() time.Time
	Random      io.Reader
	UserCallers func() []string
	Visible     func(callerID, projectKey string) bool
	// MayAct is the permission check behind a notification's ACTION BUTTONS (the
	// `interaction.answer` permission of the assembly's model): may callerID answer an
	// interaction right now? The service asks it when a token is MINTED and again when the
	// token is REDEEMED, because a permission can be revoked between the two (design §6).
	// A nil MayAct means "no permission plumbing", which fails CLOSED: no action button is
	// ever minted and no token is ever redeemed.
	MayAct                func(callerID string) bool
	AllowInsecureLoopback bool
}

// SubscriptionInput is the browser PushSubscription shape accepted by HTTP.
type SubscriptionInput struct {
	Endpoint string           `json:"endpoint"`
	Keys     SubscriptionKeys `json:"keys"`
}

type SubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type queuedEvent struct {
	scope  string
	kind   string
	detail map[string]any
	caller string
}

// Service owns process-lifetime transient state. Subscription rows and job
// interactions remain authoritative in their existing stores.
type Service struct {
	store       *jobstore.Store
	jobs        Jobs
	sender      *pushSender
	actions     *actionTokens
	now         func() time.Time
	userCallers func() []string
	visible     func(string, string) bool
	mayActFn    func(string) bool

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan queuedEvent
	wg     sync.WaitGroup

	sentMu sync.Mutex
	sentAt map[string]time.Time
}

func NewService(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Jobs == nil {
		return nil, errors.New("webpush: store and jobs are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	if opts.UserCallers == nil {
		opts.UserCallers = func() []string { return []string{"default"} }
	}
	if opts.Visible == nil {
		opts.Visible = func(string, string) bool { return true }
	}
	if opts.MayAct == nil {
		opts.MayAct = mayNeverAct
	}
	sender, err := newPushSender(opts.ConfigDir, opts.Subject, opts.HTTPClient, opts.Now, opts.Random, opts.AllowInsecureLoopback)
	if err != nil {
		return nil, err
	}
	actions, err := newActionTokens(opts.Random, opts.Now)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		store: opts.Store, jobs: opts.Jobs, sender: sender, actions: actions,
		now: opts.Now, userCallers: opts.UserCallers, visible: opts.Visible, mayActFn: opts.MayAct,
		ctx: ctx, cancel: cancel, queue: make(chan queuedEvent, eventQueueSize),
		sentAt: make(map[string]time.Time),
	}
	s.wg.Add(1)
	go s.run()
	return s, nil
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

func (s *Service) PublicKey() (string, error) {
	return s.sender.publicKey()
}

func (s *Service) Subscribe(callerID string, input SubscriptionInput, userAgent string) (jobstore.PushSubscription, error) {
	callerID = normalizeCaller(callerID)
	if err := s.validateSubscription(input); err != nil {
		return jobstore.PushSubscription{}, err
	}
	sub := jobstore.PushSubscription{
		CallerID: callerID, Endpoint: input.Endpoint,
		P256DH: input.Keys.P256DH, Auth: input.Keys.Auth,
		UserAgent: truncateRunes(userAgent, 512), CreatedAt: s.now().Unix(),
	}
	if err := s.store.UpsertPushSubscription(sub); err != nil {
		return jobstore.PushSubscription{}, err
	}
	return sub, nil
}

func (s *Service) ListSubscriptions(callerID string) ([]jobstore.PushSubscription, error) {
	return s.store.ListPushSubscriptions(normalizeCaller(callerID))
}

func (s *Service) DeleteSubscription(callerID, endpoint string) (bool, error) {
	return s.store.DeletePushSubscription(normalizeCaller(callerID), strings.TrimSpace(endpoint))
}

// NewActionToken mints the one-time token behind one action button of an interaction
// notification. The caller must hold the answer permission RIGHT NOW (design §6: 动作按钮在
// 签发和兑现时都检查) — refusing here keeps a button the caller could not use out of the
// notification altogether.
func (s *Service) NewActionToken(callerID, jobID, interactionID string, options []string) (string, error) {
	callerID = normalizeCaller(callerID)
	if !s.mayAct(callerID) {
		return "", ErrActionForbidden
	}
	return s.actions.mint(callerID, jobID, interactionID, options)
}

// mayAct is the seam itself, defaulted to "never" by NewService so a service wired without a
// permission check can never hand out a working button. dispatch.go asks it before it even
// tries to mint a token, so a caller without the permission still gets the notification —
// just without buttons it could not use.
func (s *Service) mayAct(callerID string) bool {
	return s.mayActFn != nil && s.mayActFn(callerID)
}

func (s *Service) Act(token, option string) (job.Interaction, error) {
	claims, err := s.actions.consume(token, option)
	if err != nil {
		return job.Interaction{}, err
	}
	// A token outlives nothing but its caller: once that user caller is gone
	// the notification's approval buttons must stop working too.
	if !s.hasUserCaller(claims.CallerID) {
		return job.Interaction{}, ErrActionUnauthorized
	}
	// …and once it lost the answer permission (revoked, expired, or the config-derived
	// grant changed) the button stops working as well, without waiting for a new
	// notification (design §6).
	if !s.mayAct(claims.CallerID) {
		return job.Interaction{}, ErrActionForbidden
	}
	return s.jobs.AnswerInteractionByPush(claims.JobID, claims.InteractionID, option)
}

func (s *Service) hasUserCaller(callerID string) bool {
	for _, id := range s.userCallers() {
		if normalizeCaller(id) == callerID {
			return true
		}
	}
	return false
}

// QueueTest enqueues one non-action notification to the caller's current
// subscriptions. The return value is the number of endpoints accepted for async
// processing, not a delivery receipt.
func (s *Service) QueueTest(callerID string) (int, error) {
	callerID = normalizeCaller(callerID)
	rows, err := s.store.ListPushSubscriptions(callerID)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, ErrNoSubscriptions
	}
	if !s.enqueue(queuedEvent{kind: "test", caller: callerID}) {
		return 0, errors.New("webpush: event queue is full")
	}
	return len(rows), nil
}

// ObserveEvent implements job.JobEventObserver. It never blocks the job event
// path and never performs crypto/network I/O inline.
func (s *Service) ObserveEvent(scope, eventType string, detail map[string]any) {
	if !s.enqueue(queuedEvent{scope: scope, kind: eventType, detail: cloneDetail(detail)}) {
		slog.Warn("webpush event dropped", "scope", scope, "type", eventType)
	}
}

func (s *Service) enqueue(event queuedEvent) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.queue <- event:
		return true
	default:
		return false
	}
}

func (s *Service) validateSubscription(input SubscriptionInput) error {
	if len(input.Endpoint) == 0 || len(input.Endpoint) > 4096 {
		return errors.New("webpush: endpoint is required and must be at most 4096 bytes")
	}
	parsed, err := url.Parse(input.Endpoint)
	if err != nil || parsed.Host == "" {
		return errors.New("webpush: invalid endpoint")
	}
	if parsed.Scheme != "https" &&
		!(s.sender.allowInsecureLoopback && parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return errors.New("webpush: endpoint must use https")
	}
	public, err := base64.RawURLEncoding.DecodeString(input.Keys.P256DH)
	if err != nil || len(public) != p256PublicBytes {
		return fmt.Errorf("webpush: p256dh must be a %d-byte uncompressed P-256 key", p256PublicBytes)
	}
	if _, err := ecdhPublicKey(public); err != nil {
		return err
	}
	auth, err := base64.RawURLEncoding.DecodeString(input.Keys.Auth)
	if err != nil || len(auth) != authSecretBytes {
		return fmt.Errorf("webpush: auth must be %d bytes", authSecretBytes)
	}
	return nil
}

func ecdhPublicKey(raw []byte) ([]byte, error) {
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return nil, fmt.Errorf("webpush: invalid p256dh: %w", err)
	}
	return raw, nil
}

func normalizeCaller(callerID string) string {
	if strings.TrimSpace(callerID) == "" {
		return "default"
	}
	return callerID
}

// mayNeverAct is the fail-closed default of the MayAct seam: a service assembled without a
// permission check hands out no action buttons at all.
func mayNeverAct(string) bool { return false }

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func cloneDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return nil
	}
	out := make(map[string]any, len(detail))
	for key, value := range detail {
		out[key] = value
	}
	return out
}
