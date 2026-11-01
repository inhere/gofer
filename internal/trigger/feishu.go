package trigger

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// Feishu event subscription headers the adapter reads.
const (
	FeishuTimestampHeader = "X-Lark-Request-Timestamp"
	FeishuNonceHeader     = "X-Lark-Request-Nonce"
	FeishuSignatureHeader = "X-Lark-Signature"
)

// feishuMessageEvent is the only event type that dispatches.
const feishuMessageEvent = "im.message.receive_v1"

// maxFeishuChallengeBytes bounds the challenge the handshake echoes back.
const maxFeishuChallengeBytes = 512

// feishuAdapter implements the Feishu / Lark event subscription (schema 2.0):
//   - X-Lark-Signature = hex(SHA-256(timestamp + nonce + encrypt key + raw body)), the time
//     window is auth.replay_window_sec; auth.secret_env holds the Encrypt Key;
//   - the body is {"encrypt": base64(iv || AES-256-CBC(key = SHA-256(encrypt key)))} and is
//     decrypted before parsing;
//   - the verification token (auth.verification_token_env) must equal header.token;
//   - the url_verification handshake is answered with {"challenge": ...} after the token
//     check (the platform sends no signature on it).
type feishuAdapter struct{}

func (feishuAdapter) name() string { return ProviderFeishu }

func (feishuAdapter) headers(config.TriggerConfig) []string {
	return []string{FeishuTimestampHeader, FeishuNonceHeader, FeishuSignatureHeader}
}

// SignFeishu returns the X-Lark-Signature value; shared by the adapter and tests.
func SignFeishu(encryptKey, timestamp, nonce string, body []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(timestamp + nonce + encryptKey))
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func (feishuAdapter) authenticate(tc config.TriggerConfig, req TriggerRequest, now time.Time) error {
	key := os.Getenv(tc.Auth.SecretEnv)
	if key == "" {
		return ErrUnauthenticated
	}
	ts := strings.TrimSpace(header(req.Headers, FeishuTimestampHeader))
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrUnauthenticated
	}
	nonce := strings.TrimSpace(header(req.Headers, FeishuNonceHeader))
	got, err := hex.DecodeString(strings.TrimSpace(header(req.Headers, FeishuSignatureHeader)))
	if err != nil || nonce == "" {
		return ErrUnauthenticated
	}
	want, _ := hex.DecodeString(SignFeishu(key, ts, nonce, req.RawBody))
	if !hmac.Equal(got, want) {
		return ErrUnauthenticated
	}
	if abs(now.Unix()-sec) > int64(replayWindow(tc)) {
		return ErrReplay
	}
	return nil
}

// decode decrypts an encrypted body, parses it and checks the verification token.
func (feishuAdapter) decode(tc config.TriggerConfig, req TriggerRequest) (map[string]any, error) {
	return feishuDecode(tc, req.RawBody)
}

func feishuDecode(tc config.TriggerConfig, raw []byte) (map[string]any, error) {
	var outer map[string]any
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil, ErrBodyNotJSON
	}
	payload := outer
	if enc, ok := outer["encrypt"].(string); ok {
		plain, err := feishuDecrypt(os.Getenv(tc.Auth.SecretEnv), enc)
		if err != nil {
			return nil, ErrBodyNotJSON
		}
		payload = nil
		if err := json.Unmarshal(plain, &payload); err != nil {
			return nil, ErrBodyNotJSON
		}
	}
	token := os.Getenv(tc.Auth.VerificationTokenEnv)
	got := jsonString(jsonObject(payload, "header"), "token")
	if got == "" {
		got = jsonString(payload, "token")
	}
	if token == "" || !secureEqual(got, token) {
		return nil, ErrUnauthenticated
	}
	return payload, nil
}

func feishuDecrypt(encryptKey, b64 string) ([]byte, error) {
	if encryptKey == "" {
		return nil, ErrUnauthenticated
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) < 2*aes.BlockSize || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("feishu ciphertext is malformed")
	}
	sum := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	iv, body := data[:aes.BlockSize], data[aes.BlockSize:]
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, body)
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) || !bytes.Equal(out[len(out)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, fmt.Errorf("feishu padding is invalid")
	}
	return out[:len(out)-pad], nil
}

// FeishuEncrypt is the platform's encryption, used by tests to build encrypted fixtures.
func FeishuEncrypt(encryptKey string, plain, iv []byte) string {
	sum := sha256.Sum256([]byte(encryptKey))
	block, _ := aes.NewCipher(sum[:])
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	buf := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(buf))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, buf)
	return base64.StdEncoding.EncodeToString(append(append([]byte(nil), iv...), out...))
}

// handshake answers {"type":"url_verification","challenge":...}: it runs before the
// signature check (the platform does not sign it) but requires the verification token.
func (feishuAdapter) handshake(tc config.TriggerConfig, req TriggerRequest) (string, bool, error) {
	var outer map[string]any
	if err := json.Unmarshal(req.RawBody, &outer); err != nil {
		return "", false, nil
	}
	payload := outer
	if _, enc := outer["encrypt"]; enc {
		var err error
		if payload, err = feishuDecode(tc, req.RawBody); err != nil {
			// A bad token or ciphertext here is judged by the normal path.
			return "", false, nil
		}
	}
	if jsonString(payload, "type") != "url_verification" {
		return "", false, nil
	}
	token := os.Getenv(tc.Auth.VerificationTokenEnv)
	if token == "" || !secureEqual(jsonString(payload, "token"), token) {
		return "", false, ErrUnauthenticated
	}
	ch := jsonString(payload, "challenge")
	if ch == "" || len(ch) > maxFeishuChallengeBytes {
		return "", false, fmt.Errorf("%w: challenge is invalid", ErrInvalidInput)
	}
	return ch, true, nil
}

func (feishuAdapter) eventID(tc config.TriggerConfig, req TriggerRequest) (string, error) {
	return imEventID(feishuNormalize, tc, req)
}

func (feishuAdapter) filter(tc config.TriggerConfig, req TriggerRequest, _ time.Time) (string, error) {
	return imFilter(feishuNormalize, tc, req)
}

func (feishuAdapter) vars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	return imVars(feishuNormalize, tc, req)
}

// feishuNormalize reads an im.message.receive_v1 event. Other event types / schemas,
// non-text messages and messages sent by an app (the bot itself) are ignored. Leading
// @mentions (the bot) are removed from the text, later ones become "@name".
func feishuNormalize(_ config.TriggerConfig, req TriggerRequest) (Envelope, string, error) {
	p := req.Payload
	hdr := jsonObject(p, "header")
	if jsonString(p, "schema") != "2.0" || jsonString(hdr, "event_type") != feishuMessageEvent {
		return Envelope{}, IgnoredUnsupported, nil
	}
	ev := jsonObject(p, "event")
	sender := jsonObject(ev, "sender")
	if jsonString(sender, "sender_type") != "user" {
		return Envelope{}, IgnoredFromBot, nil
	}
	msg := jsonObject(ev, "message")
	if jsonString(msg, "message_type") != "text" {
		return Envelope{}, IgnoredNotText, nil
	}
	ids := jsonObject(sender, "sender_id")
	env := Envelope{Provider: ProviderFeishu, EventID: strings.TrimSpace(jsonString(hdr, "event_id")),
		ConversationID: cleanLine(jsonString(msg, "chat_id")), SenderID: cleanLine(jsonString(ids, "open_id"))}
	if env.SenderID == "" {
		env.SenderID = cleanLine(jsonString(ids, "user_id"))
	}
	if t, ok := millisTime(hdr["create_time"]); ok {
		env.Timestamp = t
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(jsonString(msg, "content")), &content); err != nil {
		return Envelope{}, IgnoredNotText, nil
	}
	text := cleanText(content.Text)
	type mention struct{ key, name string }
	var ms []mention
	if arr, ok := msg["mentions"].([]any); ok {
		for _, m := range arr {
			if mm, ok := m.(map[string]any); ok && jsonString(mm, "key") != "" {
				ms = append(ms, mention{jsonString(mm, "key"), cleanLine(jsonString(mm, "name"))})
			}
		}
	}
	for stripped := true; stripped; {
		stripped = false
		text = strings.TrimSpace(text)
		for _, m := range ms {
			if rest, ok := strings.CutPrefix(text, m.key); ok {
				text, stripped = rest, true
			}
		}
	}
	for _, m := range ms {
		text = strings.ReplaceAll(text, m.key, "@"+m.name)
	}
	env.Text = text
	return env, "", nil
}
