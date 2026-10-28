// Package trigger contains the provider-neutral, authenticated trigger dispatch core.
// HTTP and provider adapters are intentionally outside this package; P0 exposes only
// an in-process invocation seam for tests and server orchestration.
package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

var (
	ErrUnknownTrigger  = errors.New("unknown or disabled trigger")
	ErrUnauthenticated = errors.New("trigger authentication failed")
	ErrReplay          = errors.New("trigger timestamp outside replay window")
	ErrRateLimited     = errors.New("trigger rate limited")
)

type TriggerRequest struct {
	TriggerName     string
	RequestID       string
	ExternalEventID string
	ReceivedAt      time.Time
	RawBody         []byte
	Headers         map[string]string
	Payload         map[string]any
	Source          string
}

type Result struct {
	RequestID, JobID string
	Duplicate        bool
}

type SubmitFunc func(context.Context, job.JobRequest) (string, error)

type AuditFunc func(kind, triggerName, owner, requestID, eventID, jobID, code string) error

type Engine struct {
	cfg     *config.Config
	store   *jobstore.Store
	submit  SubmitFunc
	audit   AuditFunc
	limiter *Limiter
	now     func() time.Time
}

func NewEngine(cfg *config.Config, store *jobstore.Store, submit SubmitFunc) *Engine {
	e := &Engine{cfg: cfg, store: store, submit: submit, limiter: NewLimiter(), now: time.Now}
	if store != nil {
		e.audit = store.AppendTriggerAudit
	}
	return e
}
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

func (e *Engine) Invoke(ctx context.Context, req TriggerRequest) (Result, error) {
	var out Result
	var tc *config.TriggerConfig
	if e == nil || e.cfg == nil {
		return out, ErrUnknownTrigger
	}
	for i := range e.cfg.Server.Triggers {
		if e.cfg.Server.Triggers[i].Name == req.TriggerName && e.cfg.Server.Triggers[i].Enabled {
			v := e.cfg.Server.Triggers[i].Clone()
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
		return out, fmt.Errorf("trigger body exceeds limit")
	}
	if err := VerifyHMAC(*tc, req.RawBody, req.Headers, e.now()); err != nil {
		kind := "trigger.auth_failed"
		if errors.Is(err, ErrReplay) {
			kind = "trigger.replay_rejected"
		}
		e.writeAudit(kind, *tc, req, "", err)
		return out, err
	}
	if req.Payload == nil {
		if err := json.Unmarshal(req.RawBody, &req.Payload); err != nil {
			return out, fmt.Errorf("trigger body must be JSON: %w", err)
		}
	}
	if req.ExternalEventID == "" {
		return out, errors.New("external event id is required for durable deduplication")
	}
	if !e.limiter.Allow(tc.Name, tc.Rate) {
		e.writeAudit("trigger.rate_limited", *tc, req, "", ErrRateLimited)
		return out, ErrRateLimited
	}
	defer e.limiter.Done(tc.Name)
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf("trigger-%d", e.now().UnixNano())
	}
	out.RequestID = req.RequestID
	e.writeAudit("trigger.received", *tc, req, "", nil)
	hash := jobstore.HashTriggerBody(req.RawBody)
	if e.store != nil {
		ttl := tc.Dedup.TTLSec
		if ttl == 0 {
			ttl = config.DefaultTriggerDedupTTLSec
		}
		existing, duplicate, err := e.store.ClaimTriggerReceipt(jobstore.TriggerReceipt{TriggerName: tc.Name, ExternalEventID: req.ExternalEventID, RequestID: req.RequestID, BodyHash: hash, Source: req.Source, ReceivedAt: req.ReceivedAt.Unix(), ExpiresAt: req.ReceivedAt.Unix() + int64(ttl)})
		if errors.Is(err, jobstore.ErrTriggerReceiptConflict) {
			e.writeAudit("trigger.rejected", *tc, req, "", err)
			return out, err
		}
		if err != nil {
			return out, err
		}
		if duplicate {
			out.Duplicate = true
			out.JobID = existing.JobID
			e.writeAudit("trigger.duplicate", *tc, req, out.JobID, nil)
			return out, nil
		}
	}
	vars, err := resolveVars(*tc, req)
	if err != nil {
		e.finishReceipt(tc, req, "rejected", "", err)
		return out, err
	}
	if e.submit == nil {
		err = errors.New("trigger submitter is not configured")
	} else {
		jobID, submitErr := e.submit(ctx, job.JobRequest{ProjectKey: tc.Project, Template: tc.Template, TemplateVars: vars, CallerID: "", OriginCaller: strings.TrimSpace(tc.OwnerCaller), Channel: "trigger", RequestID: req.RequestID})
		out.JobID, err = jobID, submitErr
	}
	if err != nil {
		e.finishReceipt(tc, req, "rejected", "", err)
		e.writeAudit("trigger.rejected", *tc, req, "", err)
		return out, err
	}
	e.finishReceipt(tc, req, "submitted", out.JobID, nil)
	e.writeAudit("trigger.submitted", *tc, req, out.JobID, nil)
	return out, nil
}

func (e *Engine) finishReceipt(tc *config.TriggerConfig, req TriggerRequest, status, jobID string, err error) {
	if e.store != nil {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		_ = e.store.UpdateTriggerReceipt(tc.Name, req.ExternalEventID, status, jobID, msg)
	}
}
func (e *Engine) writeAudit(kind string, tc config.TriggerConfig, req TriggerRequest, jobID string, err error) {
	if e.audit == nil {
		return
	}
	code := ""
	if err != nil {
		code = err.Error()
	}
	_ = e.audit(kind, tc.Name, tc.OwnerCaller, req.RequestID, req.ExternalEventID, jobID, code)
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
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n"))
	_, _ = mac.Write(body)
	if !hmac.Equal(decoded, mac.Sum(nil)) {
		return ErrUnauthenticated
	}
	return nil
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

func resolveVars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	maxVar := tc.Limits.MaxVarBytes
	if maxVar == 0 {
		maxVar = config.DefaultTriggerMaxVarBytes
	}
	out := make(map[string]string, len(tc.Input.Vars))
	for name, ref := range tc.Input.Vars {
		value, ok := lookup(ref, req)
		if !ok {
			return nil, fmt.Errorf("trigger input %q is missing", name)
		}
		raw, err := stringify(value)
		if err != nil {
			return nil, err
		}
		if maxVar > 0 && len(raw) > maxVar {
			return nil, fmt.Errorf("trigger variable %q exceeds limit", name)
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

type bucket struct {
	at       time.Time
	count    int
	inflight int
}
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]bucket
}

func NewLimiter() *Limiter { return &Limiter{buckets: make(map[string]bucket)} }
func (l *Limiter) Allow(name string, rate config.TriggerRateConfig) bool {
	if l == nil {
		return true
	}
	if rate.PerMinute <= 0 && rate.MaxInflight <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[name]
	if b.at.IsZero() || now.Sub(b.at) >= time.Minute {
		b.at = now
		b.count = 0
	}
	if rate.PerMinute > 0 && b.count >= rate.PerMinute {
		return false
	}
	if rate.Burst > 0 && b.count >= rate.Burst {
		return false
	}
	if rate.MaxInflight > 0 && b.inflight >= rate.MaxInflight {
		return false
	}
	b.count++
	b.inflight++
	l.buckets[name] = b
	return true
}
func (l *Limiter) Done(name string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[name]
	if b.inflight > 0 {
		b.inflight--
	}
	l.buckets[name] = b
}
