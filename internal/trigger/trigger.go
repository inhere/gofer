// Package trigger contains the provider-neutral, authenticated trigger dispatch core.
// HTTP and provider adapters are intentionally outside this package; P0 exposes only
// an in-process invocation seam for tests and server orchestration.
package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

var (
	ErrUnknownTrigger  = errors.New("unknown or disabled trigger")
	ErrUnauthenticated = errors.New("trigger authentication failed")
	ErrReplay          = errors.New("trigger timestamp outside replay window")
	ErrBodyTooLarge    = errors.New("trigger body exceeds limit")
	ErrEventIDTooLong  = errors.New("trigger external event id is too long")
	// ErrEventInFlight: the same event is being handled by another request right now. The
	// sender should retry later; no job id exists yet.
	ErrEventInFlight = errors.New("trigger event is already being processed")
	// ErrBodyNotJSON: the body is not a JSON object.
	ErrBodyNotJSON = errors.New("trigger body must be a JSON object")
	// ErrInvalidInput: a declared input variable is missing or too large. The error text
	// names the variable, never its value.
	ErrInvalidInput = errors.New("trigger input is invalid")
	// ErrHeaderTooLarge: a header the trigger reads is longer than MaxHeaderValueBytes.
	ErrHeaderTooLarge = errors.New("trigger header exceeds limit")
)

// RateLimitedError is the 429 case. RetryAfter is what a Retry-After header is made of.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "trigger rate limited" }

// ErrRateLimited matches any *RateLimitedError through errors.Is.
var ErrRateLimited = &RateLimitedError{}

// Is makes errors.Is(err, ErrRateLimited) true for every rate-limit refusal.
func (e *RateLimitedError) Is(target error) bool { _, ok := target.(*RateLimitedError); return ok }

// maxEventIDBytes bounds an external event id; the stored form is a hash, so this only
// protects the request path.
const maxEventIDBytes = 512

// DefaultClaimTimeout is how long a claimed receipt may stay unfinished before a
// redelivery may take it over and the reconciler settles it.
const DefaultClaimTimeout = 5 * time.Minute

type TriggerRequest struct {
	TriggerName string
	RequestID   string
	// ExternalEventID, when set by a provider adapter, wins over dedup.key / Idempotency-Key.
	ExternalEventID string
	ReceivedAt      time.Time
	RawBody         []byte
	Headers         map[string]string
	Payload         map[string]any
	Source          string
	// Provider names the adapter ("" = generic). RemoteAddr is hashed before it reaches the
	// audit trail; it stays empty until an HTTP entry exists.
	Provider   string
	RemoteAddr string
}

type Result struct {
	RequestID, JobID string
	Duplicate        bool
	// Status is the receipt status for a duplicate (submitted / rejected).
	Status string
	// Ignored is true when an authenticated delivery was acknowledged without dispatching
	// (an event the trigger does not subscribe to, a failed condition, a ping); Reason says
	// why (Ignored* constants). No receipt exists for it and no job was created.
	Ignored bool
	Reason  string
	// Challenge is the answer of a platform endpoint-verification handshake (Feishu); the
	// HTTP entry returns it instead of the usual body.
	Challenge string
}

type SubmitFunc func(context.Context, job.JobRequest) (string, error)

// PreflightFunc judges a fixed request without submitting it (see Admission.Preflight).
type PreflightFunc func(context.Context, job.JobRequest) error

type AuditFunc func(jobstore.TriggerAuditEvent) error

type Engine struct {
	cfg          *config.Config
	cfgFn        func() *config.Config
	store        *jobstore.Store
	submit       SubmitFunc
	preflight    PreflightFunc
	audit        AuditFunc
	limiter      *Limiter
	now          func() time.Time
	claimTimeout time.Duration
}

func NewEngine(cfg *config.Config, store *jobstore.Store, submit SubmitFunc) *Engine {
	e := &Engine{cfg: cfg, store: store, submit: submit, now: time.Now, claimTimeout: DefaultClaimTimeout}
	e.limiter = NewLimiter(func() time.Time { return e.now() })
	if store != nil {
		e.audit = store.AppendTriggerAudit
	}
	return e
}

// NewServerEngine is the production assembly: the live config, the job layer behind the
// owner admission, and the durable receipts of the job store.
func NewServerEngine(cfg func() *config.Config, jobs *job.Service, can PermissionFunc) *Engine {
	adm := NewAdmission(cfg, jobs, can)
	e := NewEngine(nil, jobs.Meta(), adm.Submit)
	e.cfgFn = cfg
	e.preflight = adm.Preflight
	return e
}

// SetConfigFunc makes the engine read the configuration per call, so a reload applies.
func (e *Engine) SetConfigFunc(f func() *config.Config) { e.cfgFn = f }
func (e *Engine) SetPreflight(f PreflightFunc)          { e.preflight = f }
func (e *Engine) SetClock(now func() time.Time) {
	if now != nil {
		e.now = now
	}
}
func (e *Engine) SetLimiter(l *Limiter) {
	if l != nil {
		e.limiter = l
	}
}
func (e *Engine) SetAudit(a AuditFunc) { e.audit = a }

// SetClaimTimeout overrides DefaultClaimTimeout.
func (e *Engine) SetClaimTimeout(d time.Duration) {
	if d > 0 {
		e.claimTimeout = d
	}
}

func (e *Engine) config() *config.Config {
	if e.cfgFn != nil {
		return e.cfgFn()
	}
	return e.cfg
}

// ExternalEventID resolves the external event id of a request: the adapter's, else the
// configured dedup.key reference, else the Idempotency-Key header. An empty result means
// "no stable event id": the request is delivered at least once and not deduplicated (design
// 7a); it is not an error.
func ExternalEventID(tc config.TriggerConfig, req TriggerRequest) (string, error) {
	id := strings.TrimSpace(req.ExternalEventID)
	if id == "" && tc.Dedup.Key != "" && tc.Dedup.Key != "Idempotency-Key" {
		if v, ok := lookup(tc.Dedup.Key, req); ok {
			if str, err := stringify(v); err == nil {
				id = strings.TrimSpace(str)
			}
		}
	}
	if id == "" {
		id = strings.TrimSpace(header(req.Headers, "Idempotency-Key"))
	}
	if len(id) > maxEventIDBytes {
		return "", ErrEventIDTooLong
	}
	return id, nil
}

func (e *Engine) Invoke(ctx context.Context, req TriggerRequest) (Result, error) {
	var out Result
	var tc *config.TriggerConfig
	if e == nil {
		return out, ErrUnknownTrigger
	}
	cfg := e.config()
	if cfg == nil {
		return out, ErrUnknownTrigger
	}
	for i := range cfg.Server.Triggers {
		if cfg.Server.Triggers[i].Name == req.TriggerName && cfg.Server.Triggers[i].Enabled {
			v := cfg.Server.Triggers[i].Clone()
			tc = &v
			break
		}
	}
	if tc == nil {
		return out, ErrUnknownTrigger
	}
	if req.ReceivedAt.IsZero() {
		req.ReceivedAt = e.now()
	}
	maxBody := tc.Limits.MaxBodyBytes
	if maxBody == 0 {
		maxBody = config.DefaultTriggerMaxBodyBytes
	}
	if len(req.RawBody) > maxBody {
		return out, ErrBodyTooLarge
	}
	ad, ok := adapterFor(*tc)
	if !ok {
		return out, ErrUnknownTrigger
	}
	if req.Provider == "" {
		// The HTTP entry never sets it; an in-process caller may label its own origin.
		req.Provider = ad.name()
	}
	if hs, ok := ad.(handshaker); ok {
		challenge, handled, err := hs.handshake(*tc, req)
		if err != nil {
			e.writeAudit("trigger.auth_failed", *tc, req, "", err)
			return out, err
		}
		if handled {
			if req.RequestID == "" {
				req.RequestID = newRequestID()
			}
			out.RequestID, out.Ignored, out.Reason, out.Challenge = req.RequestID, true, IgnoredHandshake, challenge
			e.writeAudit("trigger.ignored", *tc, req, "", errors.New("ignored: "+IgnoredHandshake))
			return out, nil
		}
	}
	if err := ad.authenticate(*tc, req, e.now()); err != nil {
		kind := "trigger.auth_failed"
		if errors.Is(err, ErrReplay) {
			kind = "trigger.replay_rejected"
		}
		e.writeAudit(kind, *tc, req, "", err)
		return out, err
	}
	if req.Payload == nil {
		if dec, ok := ad.(payloadDecoder); ok {
			payload, err := dec.decode(*tc, req)
			if err != nil {
				if errors.Is(err, ErrUnauthenticated) {
					e.writeAudit("trigger.auth_failed", *tc, req, "", err)
				}
				return out, err
			}
			req.Payload = payload
		} else if err := json.Unmarshal(req.RawBody, &req.Payload); err != nil {
			return out, ErrBodyNotJSON
		}
	}
	eventID, err := ad.eventID(*tc, req)
	if err != nil {
		return out, err
	}
	// The raw id never goes below this line: receipts and audit rows carry its hash.
	req.ExternalEventID = ""
	eventKey := ""
	if eventID != "" {
		eventKey = jobstore.HashTriggerEventID(tc.Name, eventID)
		req.ExternalEventID = eventKey
	}
	// An ignored delivery is neither limited nor claimed: it starts nothing, so it must not
	// use the rate budget or the dedup slot a dispatching delivery needs.
	reason, err := ad.filter(*tc, req, e.now())
	if err != nil {
		kind := "trigger.rejected"
		if errors.Is(err, ErrReplay) {
			kind = "trigger.replay_rejected"
		}
		e.writeAudit(kind, *tc, req, "", err)
		return out, err
	}
	if reason != "" {
		if req.RequestID == "" {
			req.RequestID = newRequestID()
		}
		out.RequestID, out.Ignored, out.Reason = req.RequestID, true, reason
		e.writeAudit("trigger.ignored", *tc, req, "", errors.New("ignored: "+reason))
		return out, nil
	}
	release, dec := e.limiter.Acquire(EffectiveLimits(*tc, &cfg.Server)...)
	if !dec.OK {
		rl := &RateLimitedError{RetryAfter: dec.RetryAfter}
		e.writeAudit("trigger.rate_limited", *tc, req, "", rl)
		return out, rl
	}
	defer release()
	if req.RequestID == "" {
		req.RequestID = newRequestID()
	}
	out.RequestID = req.RequestID
	e.writeAudit("trigger.received", *tc, req, "", nil)

	vars, err := ad.vars(*tc, req)
	if err != nil {
		e.writeAudit("trigger.rejected", *tc, req, "", err)
		return out, err
	}
	// Pure checks first: a request the owner may not run must not consume the dedup claim,
	// so a redelivery after the owner is re-granted still works.
	if e.preflight != nil {
		if err := e.preflight(ctx, fixedRequest(*tc, vars, req.RequestID)); err != nil {
			e.writeAudit("trigger.rejected", *tc, req, "", err)
			return out, err
		}
	}
	if e.store != nil && eventKey != "" {
		proceed, err := e.claim(*tc, &req, &out)
		if !proceed {
			return out, err
		}
	}
	if e.submit == nil {
		err = errors.New("trigger submitter is not configured")
	} else {
		var jobID string
		jobID, err = e.submit(ctx, fixedRequest(*tc, vars, req.RequestID))
		out.JobID = jobID
	}
	if err != nil {
		status := jobstore.TriggerReceiptFailed
		if permanent(err) {
			status = jobstore.TriggerReceiptRejected
		}
		e.finishReceipt(tc, req, status, "", err)
		e.writeAudit("trigger.rejected", *tc, req, "", err)
		return out, err
	}
	e.finishReceipt(tc, req, jobstore.TriggerReceiptSubmitted, out.JobID, nil)
	e.writeAudit("trigger.submitted", *tc, req, out.JobID, nil)
	return out, nil
}

// claim takes (or re-takes) the dedup receipt of the event. proceed reports whether this
// request owns the event and must submit; otherwise out/err carry the answer.
func (e *Engine) claim(tc config.TriggerConfig, req *TriggerRequest, out *Result) (proceed bool, err error) {
	ttl := tc.Dedup.TTLSec
	if ttl == 0 {
		ttl = config.DefaultTriggerDedupTTLSec
	}
	hash := jobstore.HashTriggerBody(req.RawBody)
	recv := req.ReceivedAt.Unix()
	existing, duplicate, err := e.store.ClaimTriggerReceipt(jobstore.TriggerReceipt{TriggerName: tc.Name, ExternalEventID: req.ExternalEventID, RequestID: req.RequestID, BodyHash: hash, Source: req.Source, ReceivedAt: recv, ExpiresAt: recv + int64(ttl)})
	if errors.Is(err, jobstore.ErrTriggerReceiptConflict) {
		e.writeAudit("trigger.rejected", tc, *req, "", err)
		return false, err
	}
	if err != nil {
		return false, err
	}
	if !duplicate {
		return true, nil
	}
	if existing.Status == jobstore.TriggerReceiptFailed || existing.Status == jobstore.TriggerReceiptClaimed {
		// An earlier attempt failed or died: take the receipt over and submit again under
		// the SAME request id, which the job layer treats as an idempotency key, so a job
		// the dead attempt did create is returned instead of duplicated.
		stale := e.now().Add(-e.claimTimeout).Unix()
		won, err := e.store.ReclaimTriggerReceipt(tc.Name, req.ExternalEventID, hash, e.now().Unix(), stale)
		if err != nil {
			return false, err
		}
		if won {
			req.RequestID = existing.RequestID
			out.RequestID = existing.RequestID
			return true, nil
		}
		cur, ok, err := e.store.GetTriggerReceipt(tc.Name, req.ExternalEventID)
		if err != nil {
			return false, err
		}
		if !ok || cur.Status == jobstore.TriggerReceiptClaimed {
			e.writeAudit("trigger.duplicate", tc, *req, "", ErrEventInFlight)
			return false, ErrEventInFlight
		}
		existing = &cur
	}
	out.Duplicate = true
	out.JobID = existing.JobID
	out.Status = existing.Status
	out.RequestID = existing.RequestID
	e.writeAudit("trigger.duplicate", tc, *req, out.JobID, nil)
	return false, nil
}

// newRequestID makes a request id that is unique per request: the job layer treats it as
// an idempotency key, so two requests must never share one.
func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("trigger-%d", time.Now().UnixNano())
	}
	return "trigger-" + hex.EncodeToString(b[:])
}

// permanent reports whether a submit error will repeat on redelivery.
func permanent(err error) bool {
	return errors.Is(err, ErrOwnerDenied) || errors.Is(err, job.ErrCeilingDenied) ||
		errors.Is(err, job.ErrInvalidRequest) || errors.Is(err, job.ErrUnknownProject)
}

// fixedRequest builds the job request: every execution resource comes from the trigger
// configuration, only the declared template variables come from the payload.
func fixedRequest(tc config.TriggerConfig, vars map[string]string, requestID string) job.JobRequest {
	return job.JobRequest{ProjectKey: tc.Project, Template: tc.Template, TemplateVars: vars, CallerID: "", OriginCaller: strings.TrimSpace(tc.OwnerCaller), Channel: "trigger", RequestID: requestID}
}

func (e *Engine) finishReceipt(tc *config.TriggerConfig, req TriggerRequest, status, jobID string, err error) {
	if e.store != nil && req.ExternalEventID != "" {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		_ = e.store.UpdateTriggerReceipt(tc.Name, req.ExternalEventID, status, jobID, msg)
	}
}

// writeAudit records one event. req.ExternalEventID already holds the hash by now.
func (e *Engine) writeAudit(kind string, tc config.TriggerConfig, req TriggerRequest, jobID string, err error) {
	if e.audit == nil {
		return
	}
	code := ""
	if err != nil {
		code = err.Error()
	}
	provider := req.Provider
	if provider == "" {
		provider = "generic"
	}
	remote := ""
	if req.RemoteAddr != "" {
		remote = jobstore.HashTriggerEventID("remote", req.RemoteAddr)[:16]
	}
	_ = e.audit(jobstore.TriggerAuditEvent{Kind: kind, Trigger: tc.Name, Owner: tc.OwnerCaller, Project: tc.Project, Provider: provider, RequestID: req.RequestID, EventHash: req.ExternalEventID, JobID: jobID, Code: code, Remote: remote})
}

func VerifyHMAC(tc config.TriggerConfig, body []byte, headers map[string]string, now time.Time) error {
	secret := os.Getenv(tc.Auth.SecretEnv)
	if secret == "" {
		return ErrUnauthenticated
	}
	timestampHeader := tc.Auth.TimestampHeader
	if timestampHeader == "" {
		timestampHeader = "X-Gofer-Trigger-Timestamp"
	}
	signatureHeader := tc.Auth.SignatureHeader
	if signatureHeader == "" {
		signatureHeader = "X-Gofer-Trigger-Signature"
	}
	timestamp := header(headers, timestampHeader)
	sig := header(headers, signatureHeader)
	stamp, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return ErrUnauthenticated
	}
	window := tc.Auth.ReplayWindowSec
	if window == 0 {
		window = config.DefaultTriggerReplayWindowSec
	}
	if abs(now.Unix()-stamp) > int64(window) {
		return ErrReplay
	}
	provided := strings.TrimPrefix(strings.TrimSpace(sig), "sha256=")
	decoded, err := hex.DecodeString(provided)
	if err != nil {
		return ErrUnauthenticated
	}
	if !hmac.Equal(decoded, signMAC(secret, timestamp, body)) {
		return ErrUnauthenticated
	}
	return nil
}

func signMAC(secret, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n"))
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

// Sign returns the signature header value of the generic scheme: "sha256=" followed by the
// hex HMAC-SHA256 of timestamp + "\n" + body, with timestamp the exact header text. It is the
// one place the algorithm is written, shared by VerifyHMAC, the CLI self-test and tests.
func Sign(secret, timestamp string, body []byte) string {
	return "sha256=" + hex.EncodeToString(signMAC(secret, timestamp, body))
}
func header(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}
func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// varLimit is the byte bound of one template variable.
func varLimit(tc config.TriggerConfig) int {
	if tc.Limits.MaxVarBytes == 0 {
		return config.DefaultTriggerMaxVarBytes
	}
	return tc.Limits.MaxVarBytes
}

func resolveVars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	maxVar := varLimit(tc)
	out := make(map[string]string, len(tc.Input.Vars))
	for name, ref := range tc.Input.Vars {
		value, ok := lookup(ref, req)
		if !ok {
			return nil, fmt.Errorf("%w: %q is missing", ErrInvalidInput, name)
		}
		raw, err := stringify(value)
		if err != nil {
			return nil, err
		}
		if maxVar > 0 && len(raw) > maxVar {
			return nil, fmt.Errorf("%w: %q exceeds limit", ErrInvalidInput, name)
		}
		out[name] = raw
	}
	return out, nil
}
func lookup(ref string, req TriggerRequest) (any, bool) {
	parts := strings.SplitN(ref, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	var value any
	switch parts[0] {
	case "body":
		value = req.Payload
	case "header":
		value = header(req.Headers, parts[1])
	case "message":
		value = req.Payload["message"]
	}
	if parts[0] == "header" {
		return value, value != ""
	}
	for _, p := range strings.Split(parts[1], ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return value, value != nil
}
func stringify(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case nil:
		return "", nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}
