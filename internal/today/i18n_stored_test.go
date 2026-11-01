package today

import (
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func hanInText(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff })
}

// Reading a card with an explicit language does not depend on the server language.
func TestDecisionsInExplicitLanguageBeatsServerLanguage(t *testing.T) {
	svc, st := newTestService(t)
	svc.d.Language = func() i18n.Lang { return i18n.ZhCN }
	putJob(t, st, jobstore.JobRecord{ID: "j-rev", ProjectKey: "p1", Agent: "codex", Status: job.StatusNeedsReview, EndedAt: ago(30)})
	cards, err := svc.DecisionsIn(i18n.EN, false)
	assert.NoErr(t, err)
	c, ok := find(cards, "review:j-rev")
	assert.True(t, ok)
	assert.Eq(t, "Awaiting review", c.Tag)
	cards, _ = svc.DecisionsIn("", false)
	c, _ = find(cards, "review:j-rev")
	assert.Eq(t, "待验收", c.Tag)
}
