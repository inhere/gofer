package today

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// Text that is STORED (the archive reason of an adopted memory suggestion) follows the
// server language, not the language of whoever clicked; zh-CN stays what it always was.
func TestStoredMemoryTextFollowsServerLanguage(t *testing.T) {
	for lang, want := range map[i18n.Lang]string{
		i18n.ZhCN: "管家建议（今天卡采纳）：reason",
		i18n.EN:   "Steward suggestion (adopted from the today page): reason",
	} {
		t.Run(string(lang), func(t *testing.T) {
			svc, st := newTestService(t)
			svc.d.Language = func() i18n.Lang { return lang }
			seedMemories(t, st)
			sg, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "old-note", Action: MemoryActArchive, Reason: "reason"}, "s", "")
			assert.NoErr(t, err)
			_, err = svc.AdoptMemorySuggestion(sg.ID, "human:me")
			assert.NoErr(t, err)
			rec, _, _ := st.GetTrackerMemory("trk", "old-note")
			var tomb struct {
				ArchiveReason string `json:"archive_reason"`
			}
			assert.NoErr(t, json.Unmarshal(rec.Body, &tomb))
			assert.Eq(t, want, tomb.ArchiveReason)

			// merge: "merged into X; <reason>"
			mrg, _, err := svc.SuggestMemory(MemorySuggestInput{TrackerID: "trk", Key: "release-b", Action: MemoryActMerge, Reason: "reason", Payload: MemoryPayload{Into: "release-a"}}, "s", "")
			assert.NoErr(t, err)
			_, err = svc.AdoptMemorySuggestion(mrg.ID, "human:me")
			assert.NoErr(t, err)
			rec, _, _ = st.GetTrackerMemory("trk", "release-b")
			assert.NoErr(t, json.Unmarshal(rec.Body, &tomb))
			wantMerge := "已合并到 release-a；" + want
			if lang == i18n.EN {
				wantMerge = "Merged into release-a; " + want
			}
			assert.Eq(t, wantMerge, tomb.ArchiveReason)
			if lang == i18n.EN {
				assert.False(t, hanInText(tomb.ArchiveReason))
			}
		})
	}
}

func hanInText(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff })
}

// Reading a card with an explicit language does not depend on the server language.
func TestDecisionsInExplicitLanguageBeatsServerLanguage(t *testing.T) {
	svc, st := newTestService(t)
	svc.d.Language = func() i18n.Lang { return i18n.ZhCN }
	_, err := st.CaptureWorkItem(jobstoreCapture("x"))
	assert.NoErr(t, err)
	cards, err := svc.DecisionsIn(i18n.EN, false)
	assert.NoErr(t, err)
	c, ok := find(cards, CaptureCardKey)
	assert.True(t, ok)
	assert.Eq(t, "Capture box", c.Tag)
	cards, _ = svc.DecisionsIn("", false)
	c, _ = find(cards, CaptureCardKey)
	assert.Eq(t, "收集箱", c.Tag)
}

func jobstoreCapture(text string) jobstore.CaptureInput {
	return jobstore.CaptureInput{Text: text, Kind: jobstore.WorkKindIdea, By: "human:t"}
}
