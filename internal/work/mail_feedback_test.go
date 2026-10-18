package work

import (
	"strconv"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// 收集箱「交给会话」的回写（第二期 M6）：条目分派出去的信件送达 / 被回复时，写进条目日志。
// 回写挂在 store 的信件状态变更上（mail_feedback.go），所以这里直接驱动 store 的状态机。

func journalOf(t *testing.T, st *jobstore.Store, id string) string {
	t.Helper()
	list, err := st.ListWorkJournal(id, 100, 0)
	assert.NoErr(t, err)
	var sb strings.Builder
	for _, e := range list {
		sb.WriteString(e.Text + "\n")
	}
	return sb.String()
}

// dispatchedLetter stores the letter a dispatch to sess-a would have sent, with the item id on it.
func dispatchedLetter(t *testing.T, st *jobstore.Store, itemID, body string) jobstore.Mail {
	t.Helper()
	m, err := st.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindCaller, FromCaller: "human:a",
		ToAddress: "session:sess-a", ToKind: jobstore.MailKindSession, ToID: "sess-a",
		Body: body, CaptureItemID: itemID,
	})
	assert.NoErr(t, err)
	assert.Eq(t, itemID, m.CaptureItemID)
	return m
}

func TestMailDeliveredAndAnsweredWriteBackToTheItem(t *testing.T) {
	_, st, _ := newSvc(t) // New installs the letter observer
	c := mustCaptured(t, st, "把导出做成异步")
	letter := dispatchedLetter(t, st, c.ID, "把导出做成异步")

	// Nothing is written while the letter is only pending / claimed.
	_, ok, err := st.ClaimMail(letter.ID, "hook:sess-a", 0)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, !strings.Contains(journalOf(t, st, c.ID), "已送达"))

	// Delivered: one line, naming the letter and the recipient.
	_, ok, err = st.ConfirmMail(letter.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	txt := journalOf(t, st, c.ID)
	assert.True(t, strings.Contains(txt, "已送达"))
	assert.True(t, strings.Contains(txt, "信件 #"+itoa(letter.ID)+" "))
	assert.True(t, strings.Contains(txt, "会话 sess-a"))

	// A read stamp on an already delivered letter is not a transition: still one line.
	_, ok, err = st.MarkMailRead(letter.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, 1, strings.Count(journalOf(t, st, c.ID), "已送达"))

	// Answered: the item keeps a summary (200 runes) and the letter id, never the whole body.
	body := strings.Repeat("字", 300)
	reply, err := st.InsertMail(jobstore.MailInput{
		ThreadID: letter.ThreadID, ReplyTo: letter.ID,
		FromKind: jobstore.MailKindSession, FromID: "sess-a",
		ToAddress: "caller:human:a", ToKind: jobstore.MailKindCaller, ToCaller: "human:a",
		Body: body,
	})
	assert.NoErr(t, err)
	_, ok, err = st.MarkMailAnswered(letter.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	txt = journalOf(t, st, c.ID)
	assert.True(t, strings.Contains(txt, "信件 #"+itoa(letter.ID)+" 收到回复："))
	assert.True(t, strings.Contains(txt, strings.Repeat("字", 200)+"…"))
	assert.True(t, !strings.Contains(txt, body))
	assert.Eq(t, 1, strings.Count(txt, "收到回复"))
	_ = reply
}

func TestMailWritebackIgnoresLettersWithNoItem(t *testing.T) {
	_, st, _ := newSvc(t)
	c := mustCaptured(t, st, "没有被分派")
	// A letter that no item dispatched: its delivery is nobody's journal line.
	plain, err := st.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindCaller, FromCaller: "human:a",
		ToAddress: "session:sess-a", ToKind: jobstore.MailKindSession, ToID: "sess-a",
		Body: "手写的信",
	})
	assert.NoErr(t, err)
	_, _, err = st.ClaimMail(plain.ID, "hook:sess-a", 0)
	assert.NoErr(t, err)
	_, _, err = st.ConfirmMail(plain.ID)
	assert.NoErr(t, err)
	assert.True(t, !strings.Contains(journalOf(t, st, c.ID), "已送达"))
}

// itoa keeps the assertions above readable.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
