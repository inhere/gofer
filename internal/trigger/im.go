package trigger

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/inhere/gofer/internal/config"
)

// Why an IM delivery was acknowledged but not dispatched (the "reason" of the 200).
const (
	IgnoredHandshake      = "url_verification"
	IgnoredUnsupported    = "unsupported_event"
	IgnoredNotText        = "not_text"
	IgnoredFromBot        = "from_bot"
	IgnoredNotAllowed     = "not_allowed"
	IgnoredPrefixMismatch = "prefix_not_matched"
	IgnoredEmptyText      = "empty_text"
)

// Envelope is the provider-neutral inbound IM message (design P2.1). An IM adapter only
// verifies the platform signature and fills it; it never dispatches. Everything a template
// variable can see comes from here (config.IMVarFields), never from the raw payload.
type Envelope struct {
	// Provider is the adapter name (dingtalk / feishu).
	Provider string
	// EventID is the platform's message / event id: the durable dedup key.
	EventID string
	// Timestamp is the platform's creation time of the message (zero when absent).
	Timestamp time.Time
	// SenderID / SenderName identify the human sender (the name may be empty).
	SenderID, SenderName string
	// ConversationID is the group / chat the message was sent in.
	ConversationID string
	// Text is the plain text with the bot mention and the command prefix removed.
	Text string
	// BodyHash is the hex SHA-256 of the raw request body, for correlation without storing it.
	BodyHash string
}

// imNormalizer turns an authenticated, decoded delivery into an envelope. A non-empty
// ignore reason means "acknowledge and do nothing" (handshake, non-text, a bot's own
// message, ...); an error refuses the delivery.
type imNormalizer func(tc config.TriggerConfig, req TriggerRequest) (env Envelope, ignore string, err error)

// imEnvelope runs the platform normalizer and the shared filters: the conversation / sender
// allowlists, the command prefix and the empty-text check.
func imEnvelope(norm imNormalizer, tc config.TriggerConfig, req TriggerRequest) (Envelope, string, error) {
	env, ignore, err := norm(tc, req)
	if err != nil || ignore != "" {
		return env, ignore, err
	}
	env.BodyHash = bodyHash(req.RawBody)
	if len(tc.IM.AllowConversations) > 0 && !containsString(tc.IM.AllowConversations, env.ConversationID) {
		return env, IgnoredNotAllowed, nil
	}
	if len(tc.IM.AllowSenders) > 0 && !containsString(tc.IM.AllowSenders, env.SenderID) {
		return env, IgnoredNotAllowed, nil
	}
	text := strings.TrimSpace(env.Text)
	if p := tc.IM.CommandPrefix; p != "" {
		rest, ok := strings.CutPrefix(text, p)
		if !ok {
			return env, IgnoredPrefixMismatch, nil
		}
		text = strings.TrimSpace(rest)
	}
	if text == "" {
		return env, IgnoredEmptyText, nil
	}
	env.Text = text
	return env, "", nil
}

// imVars resolves input.vars (name: im.<field>) from the envelope. Each value is bounded by
// limits.max_var_bytes; an oversize one refuses the delivery (never truncated silently).
func imVars(norm imNormalizer, tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	env, ignore, err := imEnvelope(norm, tc, req)
	if err != nil {
		return nil, err
	}
	if ignore != "" {
		return nil, fmt.Errorf("%w: delivery is not dispatchable", ErrInvalidInput)
	}
	maxVar := varLimit(tc)
	out := make(map[string]string, len(tc.Input.Vars))
	for name, ref := range tc.Input.Vars {
		v := envelopeField(env, strings.TrimPrefix(ref, "im."))
		if maxVar > 0 && len(v) > maxVar {
			return nil, fmt.Errorf("%w: %q exceeds limit", ErrInvalidInput, name)
		}
		out[name] = v
	}
	return out, nil
}

// envelopeField is the text of one whitelisted envelope field.
func envelopeField(env Envelope, field string) string {
	switch field {
	case "provider":
		return env.Provider
	case "sender_id":
		return env.SenderID
	case "sender_name":
		return env.SenderName
	case "conversation_id":
		return env.ConversationID
	case "text":
		return env.Text
	case "timestamp":
		if env.Timestamp.IsZero() {
			return ""
		}
		return env.Timestamp.UTC().Format(time.RFC3339)
	}
	return ""
}

// imEventID is the shared adapter eventID: the envelope's event id, "" for a delivery that
// will be ignored. A dispatchable message without an id is refused: nothing would stop the
// platform's retry from starting a second job.
func imEventID(norm imNormalizer, tc config.TriggerConfig, req TriggerRequest) (string, error) {
	env, ignore, err := imEnvelope(norm, tc, req)
	if err != nil || ignore != "" {
		return "", err
	}
	if env.EventID == "" {
		return "", fmt.Errorf("%w: the message id is missing", ErrInvalidInput)
	}
	return ExternalEventID(tc, TriggerRequest{ExternalEventID: env.EventID})
}

func imFilter(norm imNormalizer, tc config.TriggerConfig, req TriggerRequest) (string, error) {
	_, ignore, err := imEnvelope(norm, tc, req)
	return ignore, err
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// secureEqual compares two secrets in constant time regardless of their lengths.
func secureEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// cleanLine makes a short identifier / name safe to put in a prompt line: control
// characters (newlines included) become spaces.
func cleanLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// cleanText keeps the line structure of a message but drops the other control characters.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == '\r' {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// jsonObject / jsonString / jsonNumber read the loosely typed decoded payload.
func jsonObject(v any, path ...string) map[string]any {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	m, _ := cur.(map[string]any)
	return m
}

func jsonString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// millisTime converts a unix-millisecond number or decimal string; ok is false otherwise.
func millisTime(v any) (time.Time, bool) {
	var ms int64
	switch x := v.(type) {
	case float64:
		ms = int64(x)
	case string:
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &ms); err != nil {
			return time.Time{}, false
		}
	default:
		return time.Time{}, false
	}
	if ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}
