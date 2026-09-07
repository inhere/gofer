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
