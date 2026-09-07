package notify

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"
)

func TestKindHelpers(t *testing.T) {
	assert.True(t, ValidKind(""))
	assert.True(t, ValidKind("DingTalk"))
	assert.True(t, ValidKind("feishu"))
	assert.False(t, ValidKind("slack"))
	assert.Eq(t, KindGeneric, NormalizeKind(""))
	assert.Eq(t, KindDingTalk, NormalizeKind(" DingTalk "))
}

func TestRenderMessagePerKind(t *testing.T) {
	m := Message{
		EventType: "session.waiting", At: 1700000000,
		Title: "会话等待回复 · repo: 修 bug", Text: "第一步做完了\n选 A 还是 B？",
		Link: "https://gofer.example.com/sessions?sid=abc", LinkLabel: "打开会话回复",
	}

	// generic: machine contract
	b, err := RenderMessage("", m)
	assert.NoErr(t, err)
	var g map[string]any
	assert.NoErr(t, json.Unmarshal(b, &g))
	assert.Eq(t, m.Title, g["title"])
	assert.Eq(t, m.Link, g["link"])
	ev := g["event"].(map[string]any)
	assert.Eq(t, "session.waiting", ev["type"])

	// dingtalk: markdown with a tappable link; title repeated in the text so a
	// keyword-mode bot sees it.
	b, err = RenderMessage(KindDingTalk, m)
	assert.NoErr(t, err)
	var d struct {
		MsgType  string            `json:"msgtype"`
		Markdown map[string]string `json:"markdown"`
	}
	assert.NoErr(t, json.Unmarshal(b, &d))
	assert.Eq(t, "markdown", d.MsgType)
	assert.Eq(t, m.Title, d.Markdown["title"])
	assert.True(t, strings.Contains(d.Markdown["text"], m.Title))
	assert.True(t, strings.Contains(d.Markdown["text"], "选 A 还是 B？"))
	assert.True(t, strings.Contains(d.Markdown["text"], "[打开会话回复]("+m.Link+")"))

	// feishu: plain text, URL inline (the client auto-links it)
	b, err = RenderMessage(KindFeishu, m)
	assert.NoErr(t, err)
	var f struct {
		MsgType string            `json:"msg_type"`
		Content map[string]string `json:"content"`
	}
	assert.NoErr(t, json.Unmarshal(b, &f))
	assert.Eq(t, "text", f.MsgType)
	assert.True(t, strings.Contains(f.Content["text"], m.Title))
	assert.True(t, strings.Contains(f.Content["text"], m.Link))

	// no link → no link line, and the default label is used when one is set
	nolink := Message{Title: "t", Text: "x"}
	b, _ = RenderMessage(KindFeishu, nolink)
	assert.False(t, strings.Contains(string(b), "打开"))

	// long text is clamped (a phone cannot read 10k chars; the link carries it)
	long := Message{Title: "t", Text: strings.Repeat("字", 5000)}
	b, _ = RenderMessage(KindFeishu, long)
	assert.NoErr(t, json.Unmarshal(b, &f))
	assert.True(t, len([]rune(f.Content["text"])) < 700)
	assert.True(t, strings.HasSuffix(f.Content["text"], "…"))
}

func TestApplyProviderAuth(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte(`{"msg_type":"text","content":{"text":"hi"}}`)

	// no secret / generic kind → untouched (generic signs via the HMAC header)
	u, b, err := ApplyProviderAuth(KindDingTalk, "https://x/y", body, "", now)
	assert.NoErr(t, err)
	assert.Eq(t, "https://x/y", u)
	assert.Eq(t, string(body), string(b))
	u, _, err = ApplyProviderAuth(KindGeneric, "https://x/y", body, "sec", now)
	assert.NoErr(t, err)
	assert.Eq(t, "https://x/y", u)

	// dingtalk: timestamp(ms) + sign in the query, existing params kept
	u, b, err = ApplyProviderAuth(KindDingTalk, "https://oapi.dingtalk.com/robot/send?access_token=tok", body, "SECabc", now)
	assert.NoErr(t, err)
	assert.Eq(t, string(body), string(b)) // body untouched
	parsed, perr := url.Parse(u)
	assert.NoErr(t, perr)
	q := parsed.Query()
	assert.Eq(t, "tok", q.Get("access_token"))
	assert.Eq(t, "1700000000000", q.Get("timestamp"))
	want := hmacB64([]byte("SECabc"), "1700000000000\nSECabc")
	assert.Eq(t, want, q.Get("sign"))
	_, derr := base64.StdEncoding.DecodeString(q.Get("sign"))
	assert.NoErr(t, derr)

	// feishu: timestamp(sec) + sign in the body; KEY is "<ts>\n<secret>", data empty
	u, b, err = ApplyProviderAuth(KindFeishu, "https://open.feishu.cn/hook/x", body, "SECabc", now)
	assert.NoErr(t, err)
	assert.Eq(t, "https://open.feishu.cn/hook/x", u) // url untouched
	var obj map[string]any
	assert.NoErr(t, json.Unmarshal(b, &obj))
	assert.Eq(t, "1700000000", obj["timestamp"])
	assert.Eq(t, hmacB64([]byte("1700000000\nSECabc"), ""), obj["sign"])
	assert.Eq(t, "text", obj["msg_type"]) // original fields preserved

	// feishu with a non-object body is an error, not a silent bad post
	_, _, err = ApplyProviderAuth(KindFeishu, "https://x/y", []byte(`["a"]`), "s", now)
	assert.Err(t, err)
}
