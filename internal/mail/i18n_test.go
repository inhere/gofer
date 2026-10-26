package mail

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestZhMailRenderingCharacterization freezes the pre-migration Chinese output. These are
// deliberately literal expectations, not comparisons against the catalogs themselves.
func TestZhMailRenderingCharacterization(t *testing.T) {
	if got, want := RenderWake(2), `<gofer-mail-wake count="2"/>`+"\n"+"有待读信件，将在本轮开始时注入，无需回复"; got != want {
		t.Fatalf("wake = %q, want %q", got, want)
	}
	letter := RenderLetter(Letter{ID: 7, From: "会话 sess-a", Topic: "主题", Urgent: true, Body: "正文", Truncated: true})
	wantLetter := `<gofer-mail id="7" from="会话 sess-a" topic="主题" urgent="true">正文` + "\n…（正文已截断，完整内容：gofer session mail read 7）</gofer-mail>"
	if letter != wantLetter {
		t.Fatalf("letter = %q, want %q", letter, wantLetter)
	}
	m := jobstore.Mail{ID: 9, ToAddress: "session:sess-a", FromKind: jobstore.MailKindSession, FromID: "sess-b", FromCaller: "alice", LastError: "地址无效", Body: "秘密"}
	if got, want := CaptureText(m, []Candidate{{ID: "sess-c", Topic: "主题", Offline: true}}), "邮件 #9 待指派：session:sess-a\n来自 会话 sess-b（调用方 alice）\n原因：地址无效\n候选：会话 sess-c（主题，可能不在线）\n正文摘录：秘密"; got != want {
		t.Fatalf("capture = %q, want %q", got, want)
	}
	if got := BounceText(m, "原因", "已转入收集箱待指派"); got != "[gofer · 退信] 信件 #9（发给 session:sess-a）没有送到：原因。\n现在：已转入收集箱待指派\n原文摘录：秘密" {
		t.Fatalf("bounce = %q", got)
	}
	if i18n.Resolve(i18n.ZhCN) != i18n.ZhCN {
		t.Fatal("zh-CN characterization setup failed")
	}
}
