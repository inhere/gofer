package notify

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/config"
)

// DefaultTriggerEvents is the trigger set a webhook subscribes to when its
// `events` list is omitted (design §5.5/D5): the terminal event and a new
// interaction — the two states most needing a human/system to step in.
var DefaultTriggerEvents = []string{"job.terminal", "interaction.created"}

// MatchWebhooks returns the webhooks in cfg that subscribe to eventType for
// projectKey (design §5.6 enqueue match): a webhook matches when
//   - its project filter is empty OR contains projectKey, AND
//   - its event filter contains eventType (or, when its event filter is empty,
//     eventType is in DefaultTriggerEvents).
//
// It does NOT consult per-project notify_enabled — that gate is applied by the
// caller (the job service has the ProjectConfig) so this stays a pure function of
// the notification config. The returned slice is the subset to enqueue one
// delivery each for.
func MatchWebhooks(cfg *config.NotificationConfig, eventType, projectKey string) []config.WebhookConfig {
	if cfg == nil || len(cfg.Webhooks) == 0 {
		return nil
	}
	var out []config.WebhookConfig
	for _, w := range cfg.Webhooks {
		if w.URL == "" {
			continue
		}
		if !projectMatches(w.Projects, projectKey) {
			continue
		}
		if !eventMatches(w.Events, eventType) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// projectMatches reports whether a webhook's project filter admits projectKey.
// An empty filter admits every project.
func projectMatches(filter []string, projectKey string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, p := range filter {
		if p == projectKey {
			return true
		}
	}
	return false
}

// eventMatches reports whether a webhook's event filter admits eventType. An
// empty filter falls back to the DefaultTriggerEvents set.
func eventMatches(filter []string, eventType string) bool {
	if len(filter) == 0 {
		filter = DefaultTriggerEvents
	}
	for _, e := range filter {
		if e == eventType {
			return true
		}
	}
	return false
}

// EventPayload / JobSummary / Payload are the webhook POST body shape (design
// §5.6). detail is the event's raw detail_json passed through as a JSON value
// (RawMessage) so the consumer sees the same structure recorded in the stream.
type EventPayload struct {
	Seq    int64           `json:"seq"`
	JobID  string          `json:"job_id"`
	Type   string          `json:"type"`
	Detail json.RawMessage `json:"detail,omitempty"`
	At     int64           `json:"at"`
}

// JobSummary is the small job snapshot attached to a webhook body (no secrets):
// just identity/routing/status so the consumer can act without a follow-up fetch.
type JobSummary struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Project  string `json:"project"`
	Agent    string `json:"agent,omitempty"`
	Runner   string `json:"runner,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// Payload is the full webhook body `{event, job}`.
type Payload struct {
	Event EventPayload `json:"event"`
	Job   JobSummary   `json:"job"`
}

// BuildBody marshals the webhook body. detailJSON is the event's detail_json
// original (may be ""); it is embedded as a raw JSON value when it is valid JSON,
// otherwise dropped (the event still carries seq/type/at). It never errors on a
// bad detail — the body is an audit/notify payload, not a strict contract.
func BuildBody(seq int64, jobID, eventType, detailJSON string, at int64, job JobSummary) ([]byte, error) {
	ev := EventPayload{Seq: seq, JobID: jobID, Type: eventType, At: at}
	if detailJSON != "" && json.Valid([]byte(detailJSON)) {
		ev.Detail = json.RawMessage(detailJSON)
	}
	return json.Marshal(Payload{Event: ev, Job: job})
}
