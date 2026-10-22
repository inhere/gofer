package notify

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// IM bot business-error parsing (gofer-map1).
//
// Both DingTalk and Feishu answer HTTP 200 even when they REFUSE the message: the
// real outcome is a code inside the JSON response body. Treating a 2xx as
// "delivered" (the behaviour before gofer-map1) recorded a refused message as
// successful — e.g. DingTalk {errcode:310000} "keywords not in content" when the
// robot's 自定义关键词 security setting does not find the keyword.
//
// Reference for the envelopes and the codes below:
//   - DingTalk 自定义机器人 / 错误码:
//     https://open.dingtalk.com/document/orgapp/custom-robot-access
//     success {"errcode":0,"errmsg":"ok"}; an error is `errcode` + `errmsg`.
//   - Feishu 自定义机器人 / 错误码:
//     https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot
//     success {"code":0,"msg":"success"} or
//     {"StatusCode":0,"StatusMessage":"success"}; an error is `code`/`msg` or
//     `StatusCode`/`StatusMessage`.
//
// Retry classification — can the SAME message succeed on a later attempt?
//   - Permanent (a retry cannot help: the payload, the secret or the token is
//     wrong, so every attempt is refused the same way): DingTalk 310000 (invalid
//     parameter: 关键词不匹配 / 签名校验失败 / 消息内容为空), 300001 (token is invalid
//     or not exist); Feishu 19001 (param invalid), 19021 (sign match fail or
//     timestamp is not within one hour from current time), 19024 (Key Words Not
//     Found).
//   - Retryable (the provider is throttling, or the cause is unknown / possibly
//     transient): DingTalk 130101 (发送速度太快而限流), Feishu 11232 (frequency
//     limited), and every code not listed above.

// ProviderError is a business rejection from an IM bot webhook: the HTTP POST
// succeeded (2xx) but the provider's own code says the message was not accepted.
// Message is the provider's original error text — it is what the delivery
// record, the log line and the console show, so the operator reads the real
// cause ("keywords not in content") instead of "status 200".
type ProviderError struct {
	// Endpoint is the adapter that was refused: KindDingTalk / KindFeishu.
	Endpoint string
	// Code is the provider's business code (non-zero).
	Code int64
	// Message is the provider's error description verbatim.
	Message string
	// Permanent reports that retrying the same message cannot change the outcome
	// (see the classification above), so the delivery fails at once.
	Permanent bool
}

func (e *ProviderError) Error() string {
	retry := "retryable"
	if e.Permanent {
		retry = "permanent"
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "no error description"
	}
	return fmt.Sprintf("%s rejected message: code %d: %s (%s)", e.Endpoint, e.Code, msg, retry)
}

// providerEnvelope is the union of the success / error JSON both bots return.
// An absent code field stays nil, so a body that carries none of them (a plain
// {"ok":true}, an HTML page from a proxy) is not mistaken for a refusal.
type providerEnvelope struct {
	ErrCode       *providerCode `json:"errcode"`
	ErrMsg        string        `json:"errmsg"`
	Code          *providerCode `json:"code"`
	Msg           string        `json:"msg"`
	StatusCode    *providerCode `json:"StatusCode"`
	StatusMessage string        `json:"StatusMessage"`
}

// providerCode accepts a code that arrives as a JSON number or as a numeric
// string (the bots have shipped both shapes; a string code must not silently
// decode as "absent", which would count as success).
type providerCode int64

func (c *providerCode) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// A non-numeric code (e.g. "invalid") is still a refusal: keep a sentinel
		// so it is classified as unknown / retryable rather than as success.
		*c = -1
		return nil
	}
	*c = providerCode(n)
	return nil
}

// CheckProviderResponse inspects a webhook response body for kind and returns a
// *ProviderError when the provider accepted the HTTP call but refused the
// message. It returns nil for the generic kind (which has no such envelope), for
// a body that is not a JSON object, and for one that carries no code field.
func CheckProviderResponse(kind string, body []byte) error {
	kind = NormalizeKind(kind)
	if kind != KindDingTalk && kind != KindFeishu {
		return nil
	}
	var env providerEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	code, message := env.businessCode(kind)
	if code == nil || *code == 0 {
		return nil
	}
	return &ProviderError{
		Endpoint:  kind,
		Code:      int64(*code),
		Message:   message,
		Permanent: providerCodeIsPermanent(kind, int64(*code)),
	}
}

// businessCode returns the provider's business code + description for kind.
// Feishu has two spellings; the first one present wins.
func (e *providerEnvelope) businessCode(kind string) (*providerCode, string) {
	switch kind {
	case KindDingTalk:
		if e.ErrCode != nil {
			return e.ErrCode, e.ErrMsg
		}
	case KindFeishu:
		if e.Code != nil {
			return e.Code, e.Msg
		}
		if e.StatusCode != nil {
			return e.StatusCode, e.StatusMessage
		}
	}
	return nil, ""
}

// providerPermanentCodes are the business codes a retry cannot fix (see the
// classification comment at the top of this file).
var providerPermanentCodes = map[string]map[int64]bool{
	KindDingTalk: {
		300001: true, // token is invalid or not exist
		310000: true, // invalid parameter: 关键词不匹配 / 签名校验失败
	},
	KindFeishu: {
		19001: true, // param invalid
		19021: true, // sign match fail or timestamp is not within one hour from current time
		19024: true, // Key Words Not Found
	},
}

func providerCodeIsPermanent(kind string, code int64) bool {
	return providerPermanentCodes[kind][code]
}
