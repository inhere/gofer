package work

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The outcome reason of a request goes back to the caller (request language, from ctx);
// the journal line written for the item stays in the item's project language.
func TestRequestOutcomeFollowsContextJournalFollowsProject(t *testing.T) {
	svc, st, w, m := reqFixture(t, jobstore.SessionRunning)
	svc.SetLanguageFor(func(string) i18n.Lang { return i18n.ZhCN })
	m.fail = errors.New("no address")
	ctx := i18n.WithLang(context.Background(), i18n.EN)
	out, err := svc.RequestSessions(ctx, w.ID, RequestOpts{})
	if err != nil {
		t.Fatal(err)
	}
	svc.WaitIdle()
	if !strings.HasPrefix(out[0].Reason, "Delivery failed (no address)") {
		t.Errorf("outcome reason = %q, want English", out[0].Reason)
	}
	var journal string
	for _, l := range journalLines(t, st, w.ID) {
		journal += l + "\n"
	}
	if !strings.Contains(journal, "未能送达会话") {
		t.Errorf("journal = %q, want the project's (zh-CN) line", journal)
	}
	// without a ctx language the outcome follows the item's project
	out, _ = svc.RequestSessions(context.Background(), w.ID, RequestOpts{})
	svc.WaitIdle()
	if !strings.HasPrefix(out[0].Reason, "送达失败（") {
		t.Errorf("outcome reason without ctx language = %q, want zh-CN", out[0].Reason)
	}
}
