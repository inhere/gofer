package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// DingTalk robot callback headers the adapter reads.
const (
	DingTalkTimestampHeader = "timestamp"
	DingTalkSignHeader      = "sign"
)

// dingtalkAdapter implements the DingTalk robot HTTP callback: the platform sends the
// message time in milliseconds (header "timestamp") and sign = base64(HMAC-SHA256(key =
// app secret, message = timestamp + "\n" + app secret)). The signature does not cover the
// body, so the time window (auth.replay_window_sec) and the msgId dedup are what stop a
// replay; run the endpoint behind TLS.
type dingtalkAdapter struct{}

func (dingtalkAdapter) name() string { return ProviderDingTalk }

func (dingtalkAdapter) headers(config.TriggerConfig) []string {
	return []string{DingTalkTimestampHeader, DingTalkSignHeader}
}

// SignDingTalk returns the "sign" header value for a timestamp (milliseconds, as sent).
func SignDingTalk(secret, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (dingtalkAdapter) authenticate(tc config.TriggerConfig, req TriggerRequest, now time.Time) error {
	secret := os.Getenv(tc.Auth.SecretEnv)
	if secret == "" {
		return ErrUnauthenticated
	}
	ts := strings.TrimSpace(header(req.Headers, DingTalkTimestampHeader))
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrUnauthenticated
	}
	got, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header(req.Headers, DingTalkSignHeader)))
	if err != nil {
		return ErrUnauthenticated
	}
	want, _ := base64.StdEncoding.DecodeString(SignDingTalk(secret, ts))
	if !hmac.Equal(got, want) {
		return ErrUnauthenticated
	}
	if abs(now.UnixMilli()-ms) > int64(replayWindow(tc))*1000 {
		return ErrReplay
	}
	return nil
}

func replayWindow(tc config.TriggerConfig) int {
	if tc.Auth.ReplayWindowSec == 0 {
		return config.DefaultTriggerReplayWindowSec
	}
	return tc.Auth.ReplayWindowSec
}

func (dingtalkAdapter) eventID(tc config.TriggerConfig, req TriggerRequest) (string, error) {
	return imEventID(dingtalkNormalize, tc, req)
}

func (dingtalkAdapter) filter(tc config.TriggerConfig, req TriggerRequest, _ time.Time) (string, error) {
	return imFilter(dingtalkNormalize, tc, req)
}

func (dingtalkAdapter) vars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	return imVars(dingtalkNormalize, tc, req)
}

// dingtalkNormalize reads the robot callback body: msgtype, msgId, conversationId,
// senderStaffId / senderId, senderNick, createAt (ms), chatbotUserId and text.content.
// The robot's own messages (senderId == chatbotUserId) and every non-text message type are
// ignored. The sessionWebhook the platform sends is never read: nothing replies to IM.
func dingtalkNormalize(_ config.TriggerConfig, req TriggerRequest) (Envelope, string, error) {
	p := req.Payload
	if jsonString(p, "msgtype") != "text" {
		return Envelope{}, IgnoredNotText, nil
	}
	senderID := jsonString(p, "senderId")
	if bot := jsonString(p, "chatbotUserId"); bot != "" && senderID == bot {
		return Envelope{}, IgnoredFromBot, nil
	}
	env := Envelope{Provider: ProviderDingTalk, EventID: strings.TrimSpace(jsonString(p, "msgId")),
		ConversationID: cleanLine(jsonString(p, "conversationId")), SenderName: cleanLine(jsonString(p, "senderNick"))}
	env.SenderID = cleanLine(jsonString(p, "senderStaffId"))
	if env.SenderID == "" {
		env.SenderID = cleanLine(senderID)
	}
	if t, ok := millisTime(p["createAt"]); ok {
		env.Timestamp = t
	}
	env.Text = cleanText(jsonString(jsonObject(p, "text"), "content"))
	return env, "", nil
}
