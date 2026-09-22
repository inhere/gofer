package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// Webhook kinds (OBS-07a). `generic` is the original gofer contract: the
// `{event, job}` JSON body signed with an X-Gofer-Signature header. The other
// kinds are IM bot adapters — they speak the provider's own message JSON, so a
// group bot renders a readable card instead of rejecting an unknown payload.
// The canonical list lives in internal/config (next to the yaml field it
// validates); these are aliases so the renderers read naturally.
const (
	KindGeneric  = config.WebhookKindGeneric
	KindDingTalk = config.WebhookKindDingTalk
	KindFeishu   = config.WebhookKindFeishu
)

// ValidKind reports whether k is a supported webhook kind ("" = generic).
func ValidKind(k string) bool { return config.ValidWebhookKind(k) }

// NormalizeKind maps "" to KindGeneric and lowercases the rest.
func NormalizeKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return KindGeneric
	}
	return k
}

// Message is the provider-neutral notification an IM adapter renders. Title is
// one line; Text may be multi-line; Link, when set, is appended as a tappable
// link (that is why DingTalk uses markdown rather than text). EventType is
// echoed in the X-Gofer-Event header and in the generic body.
type Message struct {
	EventType string
	Title     string
	Text      string
	Link      string
	LinkLabel string
	// At is the event time (unix seconds); 0 = omit from the generic body.
	At int64
}

// maxTextRunes clamps the quoted body of a notification. IM bots reject very
// large messages and a phone cannot read them anyway; the link carries the rest.
const maxTextRunes = 500

// RenderMessage builds the POST body for kind. The provider signature is NOT
// applied here (it is time-sensitive): call ApplyProviderAuth at post time.
func RenderMessage(kind string, m Message) ([]byte, error) {
	switch NormalizeKind(kind) {
	case KindDingTalk:
		return renderDingTalk(m)
	case KindFeishu:
		return renderFeishu(m)
	default:
		return renderGeneric(m)
	}
}

// PlanMessage renders a plan-scope event (scope `plan:<id>`) as the short message an IM
// bot shows (PLAN-03). plan.blocked is the one in the default trigger set: a chain job
// failed and the plan parked on its item, so the message names the ITEM, the JOB and the
// reason — the three things the person who has to unblock it needs. Every other plan
// event (a queued item, a completed chain) renders as its own type with whatever ids its
// detail carries; ok=false for a non-plan event, so a caller falls back to the job shape.
func PlanMessage(eventType, detailJSON string, at int64) (Message, bool) {
	if !strings.HasPrefix(eventType, "plan.") {
		return Message{}, false
	}
	var d struct {
		PlanID string   `json:"plan_id"`
		TodoID string   `json:"todo_id"`
		Job    string   `json:"job"`
		Reason string   `json:"reason"`
		After  []string `json:"after"`
	}
	if detailJSON != "" {
		_ = json.Unmarshal([]byte(detailJSON), &d)
	}
	parts := make([]string, 0, 3)
	if d.TodoID != "" {
		parts = append(parts, "todo "+d.TodoID)
	}
	if d.Job != "" {
		parts = append(parts, "job "+d.Job)
	}
	if len(d.After) > 0 {
		parts = append(parts, "after "+strings.Join(d.After, ","))
	}
	if d.Reason != "" {
		parts = append(parts, d.Reason)
	}
	title := eventType
	if eventType == "plan.blocked" {
		title = "plan blocked"
	}
	return Message{EventType: eventType, Title: title, Text: strings.Join(parts, " · "), At: at}, true
}

// TransferMessage renders a file-transfer event (xfer.put|xfer.get) as the short
// message an IM bot shows: WHO moved WHAT, from/to which machine and project, how
// big. ok=false for any other event type, so a caller falls back to the job shape
// (a transfer has no job, no status and no console page to link to).
//
// detailJSON is the transfer's audit detail as recorded (the xfer event's own
// fields); an unparseable one still yields a message naming the event type, never an
// error — this is a notification, not a contract.
func TransferMessage(eventType, detailJSON string, at int64) (Message, bool) {
	if !strings.HasPrefix(eventType, "xfer.") {
		return Message{}, false
	}
	var d struct {
		Op      string `json:"op"`
		Runner  string `json:"runner"`
		Project string `json:"project"`
		Path    string `json:"path"`
		Size    int64  `json:"size"`
		By      string `json:"by"`
	}
	if detailJSON != "" {
		_ = json.Unmarshal([]byte(detailJSON), &d)
	}
	parts := make([]string, 0, 5)
	if d.By != "" {
		parts = append(parts, "by "+d.By)
	}
	if d.Runner != "" {
		parts = append(parts, "runner "+d.Runner)
	}
	if d.Project != "" {
		parts = append(parts, "project "+d.Project)
	}
	if d.Path != "" {
		parts = append(parts, "path "+d.Path)
	}
	if d.Size > 0 {
		parts = append(parts, humanSize(d.Size))
	}
	title := eventType
	switch d.Op {
	case "put":
		title = "file pushed"
	case "get":
		title = "file pulled"
	}
	return Message{EventType: eventType, Title: title, Text: strings.Join(parts, " · "), At: at}, true
}

// RetryMessage renders job.retry_exhausted (R2/AUTO-03, design §二.3) as the short
// message an IM bot shows: WHICH job gave up, how many attempts were made and the
// last exit code — the three facts the person who now has to take the work over
// needs. It is a default trigger, so this is the one shape that reaches a channel
// nobody configured.
//
// ok=false for every other event type, so the caller falls back to the generic job
// line (job.retry_scheduled / job.retry_started are readable as-is: what matters
// about them is the job, not a number).
func RetryMessage(eventType, detailJSON string, job JobSummary, at int64) (Message, bool) {
	if eventType != "job.retry_exhausted" {
		return Message{}, false
	}
	var d struct {
		Attempts int `json:"attempts"`
	}
	if detailJSON != "" {
		_ = json.Unmarshal([]byte(detailJSON), &d)
	}
	parts := make([]string, 0, 4)
	if job.ID != "" {
		parts = append(parts, "job "+job.ID)
	}
	if job.Project != "" {
		parts = append(parts, "project "+job.Project)
	}
	if job.Agent != "" {
		parts = append(parts, "agent "+job.Agent)
	}
	if d.Attempts > 0 {
		parts = append(parts, strconv.Itoa(d.Attempts)+" attempts")
	}
	parts = append(parts, "exit "+strconv.Itoa(job.ExitCode))
	return Message{
		EventType: eventType,
		Title:     "job retry exhausted",
		Text:      strings.Join(parts, " · "),
		At:        at,
	}, true
}

// humanSize renders a byte count for a message: IM notifications are read on a phone,
// where two significant digits are all that fits.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return strconv.FormatInt(n, 10) + "B"
	case n < 1024*1024:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + "KB"
	case n < 1024*1024*1024:
		return strconv.FormatFloat(float64(n)/(1024*1024), 'f', 1, 64) + "MB"
	default:
		return strconv.FormatFloat(float64(n)/(1024*1024*1024), 'f', 2, 64) + "GB"
	}
}

func clampText(s string) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= maxTextRunes {
		return string(rs)
	}
	return string(rs[:maxTextRunes]) + "…"
}

func (m Message) linkLabel() string {
	if strings.TrimSpace(m.LinkLabel) != "" {
		return m.LinkLabel
	}
	return "在 gofer 打开"
}

// renderGeneric is the shape a plain HTTP endpoint receives for a non-job
// event (job events keep the richer BuildBody payload).
func renderGeneric(m Message) ([]byte, error) {
	out := map[string]any{
		"event": map[string]any{"type": m.EventType, "at": m.At},
		"title": m.Title,
		"text":  m.Text,
	}
	if m.Link != "" {
		out["link"] = m.Link
	}
	return json.Marshal(out)
}

// renderDingTalk builds a markdown message. Markdown (not text) is used so the
// link is tappable in the DingTalk client. The title is repeated inside the
// text because DingTalk's "custom keyword" security mode matches on the message
// content, and a keyword placed in the title alone is not always seen.
func renderDingTalk(m Message) ([]byte, error) {
	var b strings.Builder
	b.WriteString("### ")
	b.WriteString(m.Title)
	b.WriteString("\n\n")
	if t := clampText(m.Text); t != "" {
		b.WriteString("> ")
		b.WriteString(strings.ReplaceAll(t, "\n", "\n> "))
		b.WriteString("\n\n")
	}
	if m.Link != "" {
		fmt.Fprintf(&b, "[%s](%s)", m.linkLabel(), m.Link)
	}
	return json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"title": m.Title, "text": b.String()},
	})
}

// renderFeishu builds a plain text message: the Feishu client auto-links a bare
// URL, so text keeps the payload simple and keyword-mode friendly.
func renderFeishu(m Message) ([]byte, error) {
	var b strings.Builder
	b.WriteString(m.Title)
	if t := clampText(m.Text); t != "" {
		b.WriteString("\n\n")
		b.WriteString(t)
	}
	if m.Link != "" {
		b.WriteString("\n\n")
		b.WriteString(m.linkLabel())
		b.WriteString("：")
		b.WriteString(m.Link)
	}
	return json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": b.String()},
	})
}

// ApplyProviderAuth applies the provider's own signature to a rendered delivery
// just before it is posted (both schemes bind a fresh timestamp, so this cannot
// happen at enqueue time). secret == "" means the bot uses keyword or IP-allowlist
// security instead — nothing to do.
//
//   - DingTalk: sign = base64(HMAC-SHA256(key=secret, data="<ms-timestamp>\n<secret>")),
//     carried as the `timestamp` + `sign` query parameters.
//   - Feishu: sign = base64(HMAC-SHA256(key="<sec-timestamp>\n<secret>", data="")),
//     carried as the `timestamp` + `sign` fields of the JSON body.
//
// It returns the URL and body to actually post; for the generic kind both are
// returned unchanged (its HMAC lives in the X-Gofer-Signature header instead).
func ApplyProviderAuth(kind, rawURL string, body []byte, secret string, now time.Time) (string, []byte, error) {
	kind = NormalizeKind(kind)
	if secret == "" || kind == KindGeneric {
		return rawURL, body, nil
	}
	switch kind {
	case KindDingTalk:
		ts := strconv.FormatInt(now.UnixMilli(), 10)
		sign := hmacB64([]byte(secret), ts+"\n"+secret)
		u, err := url.Parse(rawURL)
		if err != nil {
			return "", nil, fmt.Errorf("dingtalk: invalid url: %w", err)
		}
		q := u.Query()
		q.Set("timestamp", ts)
		q.Set("sign", sign)
		u.RawQuery = q.Encode()
		return u.String(), body, nil
	case KindFeishu:
		ts := strconv.FormatInt(now.Unix(), 10)
		// Feishu's unusual scheme: the KEY is "<timestamp>\n<secret>" and the
		// signed message is the empty string.
		sign := hmacB64([]byte(ts+"\n"+secret), "")
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err != nil {
			return "", nil, fmt.Errorf("feishu: body is not a JSON object: %w", err)
		}
		obj["timestamp"] = ts
		obj["sign"] = sign
		signed, err := json.Marshal(obj)
		if err != nil {
			return "", nil, fmt.Errorf("feishu: re-encode body: %w", err)
		}
		return rawURL, signed, nil
	}
	return rawURL, body, nil
}

func hmacB64(key []byte, data string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
