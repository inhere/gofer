package today

import (
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// heldMail inserts one letter nobody could receive and parks it as held (that is what the
// mailbox does when an address matches no unique recipient or the deadline passes).
func heldMail(t *testing.T, st *jobstore.Store, in jobstore.MailInput) jobstore.Mail {
	t.Helper()
	m, err := st.InsertMail(in)
	assert.NoErr(t, err)
	held, ok, err := st.HoldMail(m.ID, "topic:nobody matched no deliverable session")
	assert.NoErr(t, err)
	assert.True(t, ok)
	return held
}

func TestMailHeldSummaryCard(t *testing.T) {
	svc, st := newTestService(t)

	// No held letter: no card.
	cards, err := svc.Decisions(false)
	assert.NoErr(t, err)
	_, ok := find(cards, MailHeldCardKey)
	assert.False(t, ok)

	first := heldMail(t, st, jobstore.MailInput{
		FromKind: jobstore.MailKindSession, FromID: "s-abcdef1234567", FromCaller: "c1",
		ToAddress: "topic:nobody", Body: "第一封没人收的信",
	})
	second := heldMail(t, st, jobstore.MailInput{
		FromKind: jobstore.MailKindCaller, FromCaller: "c2",
		ToAddress: "path:nowhere", Body: "第二封没人收的信",
	})

	cards, err = svc.Decisions(false)
	assert.NoErr(t, err)
	sum, ok := find(cards, MailHeldCardKey)
	assert.True(t, ok)
	assert.Eq(t, KindMailHeld, sum.Kind)
	assert.Eq(t, "信箱", sum.Tag)
	assert.Eq(t, "信件 2 封待指派", sum.Title)
	assert.Eq(t, []string{"open"}, actionIDs(sum.Actions))
	assert.Eq(t, MailHeldLink, sum.Actions[0].Value)
	// The summary line describes the NEWEST held letter (the second one).
	assert.Eq(t, "调用方 c2 → path:nowhere：第二封没人收的信", sum.Summary)
	// WaitingSince is the oldest letter's creation, activity the newest one's.
	assert.Eq(t, first.CreatedAt, sum.WaitingSince)
	assert.Eq(t, second.CreatedAt, sum.ActivityAt)

	alive, err := svc.cardAlive(MailHeldCardKey)
	assert.NoErr(t, err)
	assert.True(t, alive)

	// Readdressing / cancelling one letter (held → cancelled) drops the count.
	cancelled, ok, err := st.CancelMail(second.ID, "cancelled by the test")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, jobstore.MailCancelled, cancelled.State)
	cards, _ = svc.Decisions(false)
	sum, _ = find(cards, MailHeldCardKey)
	assert.Eq(t, "信件 1 封待指派", sum.Title)

	// A letter that is only pending (in flight) is not 「待指派」 and must not be counted.
	_, err = st.InsertMail(jobstore.MailInput{
		FromKind: jobstore.MailKindSession, FromID: "s1", ToKind: jobstore.MailKindSession, ToID: "s2",
		ToAddress: "session:s2", Body: "投递中的信",
	})
	assert.NoErr(t, err)
	cards, _ = svc.Decisions(false)
	sum, _ = find(cards, MailHeldCardKey)
	assert.Eq(t, "信件 1 封待指派", sum.Title)
}

func TestMailHeldSummaryLineFallsBackWithoutAnAddress(t *testing.T) {
	st := jobstore.Mail{}
	assert.Eq(t, "未知来源 → （无地址）：", mailHeldSummary(i18n.ZhCN, st))
	assert.Eq(t, "会话 s1 → session:s2：看这个", mailHeldSummary(i18n.ZhCN, jobstore.Mail{
		FromKind: jobstore.MailKindSession, FromID: "s1", ToAddress: "session:s2", Body: " 看这个 ",
	}))
	assert.Eq(t, "job job-1 → job:j2：把这一行", mailHeldSummary(i18n.ZhCN, jobstore.Mail{
		FromKind: jobstore.MailKindJob, FromID: "job-1", ToAddress: "job:j2", Body: " 把这一行\n压成一行 ",
	}))
}
