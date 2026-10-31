package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// GitHub request headers the adapter reads.
const (
	GitHubSignatureHeader = "X-Hub-Signature-256"
	GitHubDeliveryHeader  = "X-GitHub-Delivery"
	GitHubEventHeader     = "X-GitHub-Event"
)

// Why a github delivery was acknowledged but not dispatched (the "reason" of the 200).
const (
	IgnoredPing           = "ping"
	IgnoredEventNotListed = "event_not_listed"
	IgnoredConditionUnmet = "condition_not_met"
)

// githubAdapter implements GitHub-compatible webhooks: X-Hub-Signature-256 is
// "sha256=" + hex(HMAC-SHA256(secret, raw body)), there is no timestamp, and replays are
// stopped by the durable dedup on X-GitHub-Delivery (plus the optional github.max_age_sec).
type githubAdapter struct{}

func (githubAdapter) name() string { return ProviderGitHub }

func (githubAdapter) headers(config.TriggerConfig) []string {
	return []string{GitHubSignatureHeader, GitHubDeliveryHeader, GitHubEventHeader}
}

// SignGitHub returns the X-Hub-Signature-256 value of body; shared by the adapter and tests.
func SignGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (githubAdapter) authenticate(tc config.TriggerConfig, req TriggerRequest, _ time.Time) error {
	secret := os.Getenv(tc.Auth.SecretEnv)
	if secret == "" {
		return ErrUnauthenticated
	}
	hexSig, ok := strings.CutPrefix(strings.TrimSpace(header(req.Headers, GitHubSignatureHeader)), "sha256=")
	if !ok {
		return ErrUnauthenticated
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return ErrUnauthenticated
	}
	want, _ := hex.DecodeString(strings.TrimPrefix(SignGitHub(secret, req.RawBody), "sha256="))
	if !hmac.Equal(got, want) {
		return ErrUnauthenticated
	}
	return nil
}

func (githubAdapter) eventID(tc config.TriggerConfig, req TriggerRequest) (string, error) {
	id := strings.TrimSpace(header(req.Headers, GitHubDeliveryHeader))
	if id == "" {
		// Without the delivery id nothing stops a replay of a captured signed request.
		return "", fmt.Errorf("%w: %s header is missing", ErrInvalidInput, GitHubDeliveryHeader)
	}
	return ExternalEventID(tc, TriggerRequest{ExternalEventID: id})
}

func (githubAdapter) filter(tc config.TriggerConfig, req TriggerRequest, now time.Time) (string, error) {
	event := strings.TrimSpace(header(req.Headers, GitHubEventHeader))
	if event == "" {
		return "", fmt.Errorf("%w: %s header is missing", ErrInvalidInput, GitHubEventHeader)
	}
	if event == "ping" {
		return IgnoredPing, nil
	}
	action, _ := req.Payload["action"].(string)
	keys := []string{event}
	if action != "" {
		keys = append(keys, event+"."+action)
	}
	if !githubKeyListed(keys, tc.GitHub.Events) {
		return IgnoredEventNotListed, nil
	}
	for _, c := range tc.GitHub.Conditions {
		if !githubKeyListed(keys, []string{c.Event}) {
			continue
		}
		v, ok := githubScalar(githubPath(req.Payload, c.Field))
		if !ok || !containsString(c.Equals, v) {
			return IgnoredConditionUnmet, nil
		}
	}
	if tc.GitHub.MaxAgeSec > 0 {
		if at, ok := githubEventTime(req.Payload); ok && now.Sub(at) > time.Duration(tc.GitHub.MaxAgeSec)*time.Second {
			return "", ErrReplay
		}
	}
	return "", nil
}

func githubKeyListed(keys, list []string) bool {
	for _, k := range keys {
		if containsString(list, k) {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// githubTimePaths are the payload times max_age_sec looks at, in order; the first one that
// parses is the event time.
var githubTimePaths = []string{
	"check_run.completed_at", "workflow_run.updated_at", "comment.updated_at", "review.submitted_at",
	"pull_request.updated_at", "issue.updated_at", "check_suite.updated_at", "head_commit.timestamp",
}

func githubEventTime(payload map[string]any) (time.Time, bool) {
	for _, p := range githubTimePaths {
		if s, ok := githubPath(payload, p).(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// githubVarPaths maps each whitelisted variable to the payload paths it is read from (first
// scalar found wins). This table is the whole of what a github delivery can put into a
// prompt; event and action come from the header / payload action.
var githubVarPaths = map[string][]string{
	"repository":  {"repository.full_name"},
	"number":      {"number", "pull_request.number", "issue.number", "check_run.pull_requests.0.number", "workflow_run.pull_requests.0.number"},
	"title":       {"pull_request.title", "issue.title", "check_run.name", "workflow_run.display_title"},
	"url":         {"comment.html_url", "review.html_url", "pull_request.html_url", "issue.html_url", "check_run.html_url", "workflow_run.html_url", "repository.html_url"},
	"sender":      {"sender.login"},
	"branch":      {"pull_request.head.ref", "check_run.check_suite.head_branch", "workflow_run.head_branch", "ref"},
	"base_branch": {"pull_request.base.ref"},
	"sha":         {"pull_request.head.sha", "check_run.head_sha", "workflow_run.head_sha", "after"},
	"conclusion":  {"check_run.conclusion", "workflow_run.conclusion"},
}

// vars resolves input.vars (name: github.<field>). A field the event does not carry is the
// empty string rather than an error: GitHub would otherwise see a failed delivery for a
// perfectly valid event, and redelivering cannot fix it.
func (githubAdapter) vars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	maxVar := varLimit(tc)
	out := make(map[string]string, len(tc.Input.Vars))
	for name, ref := range tc.Input.Vars {
		field := strings.TrimPrefix(ref, "github.")
		v := githubVar(field, req)
		if maxVar > 0 && len(v) > maxVar {
			return nil, fmt.Errorf("%w: %q exceeds limit", ErrInvalidInput, name)
		}
		out[name] = v
	}
	return out, nil
}

func githubVar(field string, req TriggerRequest) string {
	switch field {
	case "event":
		return strings.TrimSpace(header(req.Headers, GitHubEventHeader))
	case "action":
		a, _ := req.Payload["action"].(string)
		return a
	}
	for _, p := range githubVarPaths[field] {
		if v, ok := githubScalar(githubPath(req.Payload, p)); ok {
			if field == "branch" {
				v = strings.TrimPrefix(v, "refs/heads/")
			}
			return v
		}
	}
	return ""
}

// githubPath walks object keys and array indexes ("check_run.pull_requests.0.number").
func githubPath(payload map[string]any, path string) any {
	var cur any = payload
	for _, seg := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			cur = x[i]
		default:
			return nil
		}
	}
	return cur
}

// githubScalar is the text of a string, number or boolean; anything else (objects, arrays,
// null) is not a usable value.
func githubScalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}
