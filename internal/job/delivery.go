package job

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/notify"
)

// deliveryBackoff is the E14 per-delivery retry backoff (SR606): the wait before
// the next attempt, indexed by (attempts-1). The last entry is reused once the
// index runs past the end (until MaxAttempts retires the delivery as failed).
var deliveryBackoff = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	60 * time.Minute,
}

// deliveryClaimBatch caps how many due deliveries one sweep claims+posts, so a
// huge backlog is drained over several ticks rather than blocking one tick.
const deliveryClaimBatch = 50

// nextBackoff returns the wait before the next retry given the attempt count just
// recorded (1-based). It clamps to the last table entry.
func nextBackoff(attempts int) time.Duration {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(deliveryBackoff) {
		idx = len(deliveryBackoff) - 1
	}
	return deliveryBackoff[idx]
}

// DeliverDue runs one sweep of the E14 webhook delivery queue (design §5.6): it
// claims the due pending deliveries (SR303 single-claim via ClaimDueDeliveries),
// POSTs each to its webhook target, and records the outcome:
//   - 2xx => MarkDelivered;
//   - non-2xx / network / timeout => attempts++, and either MarkRetry with the
//     backoff next_retry_at (SR606) or, once attempts >= MaxAttempts, MarkFailed.
//
// It returns the number of deliveries processed (claimed) this sweep. A nil
// notification config means nothing to do (returns 0). ctx bounds the whole sweep;
// each POST additionally gets a per-attempt timeout. The clock is taken from
// s.nowFn so tests can pin time.
func (s *Service) DeliverDue(ctx context.Context) int {
	cfg := s.config()
	if cfg == nil || cfg.Server.Notification == nil || len(cfg.Server.Notification.Webhooks) == 0 {
		return 0
	}
	nconf := cfg.Server.Notification
	now := s.nowFn().Unix()

	claimed, err := s.meta.ClaimDueDeliveries(now, deliveryClaimBatch, jobstore.ClaimLeaseSeconds)
	if err != nil {
		slog.Warn("DeliverDue: claim due deliveries", "err", err)
		return 0
	}
	maxAttempts := nconf.EffectiveMaxAttempts()
	for _, d := range claimed {
		if ctx.Err() != nil {
			break
		}
		s.deliverOne(ctx, d, nconf, maxAttempts)
	}
	return len(claimed)
}

// deliverOne posts a single claimed delivery and records its outcome. It builds
// the webhook body from the delivery's event + a job summary, resolves the
// webhook's HMAC secret from its secret_env, then POSTs under a per-attempt
// timeout. All failures are recorded (retry/failed); none panic.
func (s *Service) deliverOne(ctx context.Context, d jobstore.Delivery, nconf *config.NotificationConfig, maxAttempts int) {
	body, eventType, ok := s.buildDeliveryBody(d)
	if !ok {
		// The source event is gone (pruned) — the delivery can never be built; retire
		// it as failed so it stops being claimed.
		_ = s.meta.MarkFailed(d.ID, d.Attempts+1, "source event missing", s.nowFn().Unix())
		return
	}
	w := s.webhookForTarget(nconf, d.Target)
	kind := notify.NormalizeKind(w.Kind)
	secret := ""
	if w.SecretEnv != "" {
		secret = os.Getenv(w.SecretEnv)
	}
	// Generic webhooks authenticate with an HMAC header over the body; IM bots
	// carry the provider's own signature in the URL (DingTalk) or the body
	// (Feishu), bound to a fresh timestamp — hence applied here, not at enqueue.
	target, hmacSecret := d.Target, secret
	if kind != notify.KindGeneric {
		hmacSecret = ""
		signedURL, signedBody, sErr := notify.ApplyProviderAuth(kind, d.Target, body, secret, s.nowFn())
		if sErr != nil {
			_ = s.meta.MarkFailed(d.ID, d.Attempts+1, "sign: "+sErr.Error(), s.nowFn().Unix())
			return
		}
		target, body = signedURL, signedBody
	}

	postCtx, cancel := context.WithTimeout(ctx, notify.DefaultPostTimeoutSeconds*time.Second)
	defer cancel()
	post := s.postFn
	if post == nil {
		post = notify.PostWebhook
	}
	err := post(postCtx, target, eventType, body, hmacSecret, *nconf)
	now := s.nowFn().Unix()
	if err == nil {
		if mErr := s.meta.MarkDelivered(d.ID, now); mErr != nil {
			slog.Warn("DeliverDue: mark delivered", "delivery_id", d.ID, "err", mErr)
		}
		return
	}

	attempts := d.Attempts + 1
	if attempts >= maxAttempts {
		if mErr := s.meta.MarkFailed(d.ID, attempts, err.Error(), now); mErr != nil {
			slog.Warn("DeliverDue: mark failed", "delivery_id", d.ID, "err", mErr)
		}
		return
	}
	nextAt := now + int64(nextBackoff(attempts)/time.Second)
	if mErr := s.meta.MarkRetry(d.ID, attempts, nextAt, err.Error(), now); mErr != nil {
		slog.Warn("DeliverDue: mark retry", "delivery_id", d.ID, "err", mErr)
	}
}

// buildDeliveryBody rebuilds the webhook body for a queued delivery: it loads the
// source event (by seq) for type/detail/at and a job summary (by id) for the job
// block. ok is false when the source event no longer exists.
func (s *Service) buildDeliveryBody(d jobstore.Delivery) (body []byte, eventType string, ok bool) {
	// Pre-rendered delivery (OBS-07a): post it verbatim, no events/job lookup.
	if d.Body != "" {
		return []byte(d.Body), d.EventType, true
	}
	ev, found, err := s.meta.GetEvent(d.EventSeq)
	if err != nil {
		slog.Warn("DeliverDue: get event", "seq", d.EventSeq, "err", err)
		return nil, "", false
	}
	if !found {
		return nil, "", false
	}
	var summary notify.JobSummary
	if jr, jok := s.Get(d.JobID); jok {
		summary = notify.JobSummary{
			ID:       jr.ID,
			Status:   jr.Status,
			Project:  jr.ProjectKey,
			Agent:    jr.Agent,
			Runner:   jr.Runner,
			ExitCode: jr.ExitCode,
		}
	} else {
		summary = notify.JobSummary{ID: d.JobID}
	}
	// An IM bot cannot read the `{event, job}` contract: render its own message.
	if cfg := s.config(); cfg != nil && cfg.Server.Notification != nil {
		if kind := notify.NormalizeKind(s.webhookForTarget(cfg.Server.Notification, d.Target).Kind); kind != notify.KindGeneric {
			msg := notify.Message{
				EventType: ev.Type,
				Title:     "job " + ev.Type,
				Text:      jobEventText(summary),
				Link:      s.webURL("/jobs/" + summary.ID),
				LinkLabel: "查看 job",
				At:        ev.At,
			}
			rendered, rErr := notify.RenderMessage(kind, msg)
			if rErr != nil {
				slog.Warn("DeliverDue: render im body", "seq", d.EventSeq, "kind", kind, "err", rErr)
				return nil, "", false
			}
			return rendered, ev.Type, true
		}
	}
	b, err := notify.BuildBody(ev.Seq, ev.JobID, ev.Type, ev.Detail, ev.At, summary)
	if err != nil {
		slog.Warn("DeliverDue: build body", "seq", d.EventSeq, "err", err)
		return nil, "", false
	}
	return b, ev.Type, true
}

// secretEnvForTarget finds the secret_env of the webhook whose URL matches the
// delivery target. Multiple webhooks can share a URL; the first match wins (they
// would carry the same secret in practice).
func (s *Service) secretEnvForTarget(nconf *config.NotificationConfig, target string) string {
	return s.webhookForTarget(nconf, target).SecretEnv
}

// webhookForTarget returns the configured webhook whose URL is target (the zero
// value when it was removed from the config after the delivery was enqueued —
// the delivery then posts as a generic unsigned body, which is what it was
// before kinds existed).
func (s *Service) webhookForTarget(nconf *config.NotificationConfig, target string) config.WebhookConfig {
	for _, w := range nconf.Webhooks {
		if w.URL == target {
			return w
		}
	}
	return config.WebhookConfig{}
}

// jobEventText is the human line an IM notification shows for a job event.
func jobEventText(j notify.JobSummary) string {
	parts := []string{"id " + j.ID}
	if j.Project != "" {
		parts = append(parts, "project "+j.Project)
	}
	if j.Status != "" {
		parts = append(parts, "status "+j.Status)
	}
	if j.Agent != "" {
		parts = append(parts, "agent "+j.Agent)
	}
	if j.Runner != "" {
		parts = append(parts, "runner "+j.Runner)
	}
	return strings.Join(parts, " · ")
}

// webURL joins the configured public console base with path; "" when no
// server.web_base_url is set (the notification then carries no link).
func (s *Service) webURL(path string) string {
	cfg := s.config()
	if cfg == nil {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.Server.WebBaseURL), "/")
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}
