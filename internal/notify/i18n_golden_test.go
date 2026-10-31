package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// The golden files under testdata/i18n_golden pin the notification text this package
// renders (IM titles, bodies, link labels and the truncation marker). The *.zh-CN.txt
// files were recorded from the code BEFORE it was migrated into the catalog, so a
// change to them changes what every zh-CN deployment's group chat shows. The *.en.txt
// files are the English rewrite; GOFER_GOLDEN_UPDATE=1 rewrites either after a
// deliberate edit (the zh-CN ones only when they do not exist yet).

var notifyCJK = regexp.MustCompile(`[\p{Han}]`)

func describeMessage(m Message) string {
	return fmt.Sprintf("EventType: %s\nTitle: %s\nText: %s\nLink: %s\nLinkLabel: %s\nAt: %d\n", m.EventType, m.Title, m.Text, m.Link, m.LinkLabel, m.At)
}

// bodyText decodes an IM payload to the text a person reads.
func bodyText(t *testing.T, kind string, raw []byte) string {
	t.Helper()
	var d struct {
		Markdown map[string]string `json:"markdown"`
		Content  map[string]string `json:"content"`
		Title    string            `json:"title"`
		Text     string            `json:"text"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case KindDingTalk:
		return d.Markdown["text"]
	case KindFeishu:
		return d.Content["text"]
	}
	return d.Title + "\n" + d.Text
}

func renderAll(t *testing.T, m Message, maxRunes int) string {
	t.Helper()
	var b strings.Builder
	for _, kind := range []string{KindDingTalk, KindFeishu, KindGeneric} {
		raw, err := RenderMessageWithLimit(kind, m, "", maxRunes)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n", kind, bodyText(t, kind, raw))
	}
	return b.String()
}

func notifyTexts(t *testing.T, lang i18n.Lang) map[string]string {
	t.Helper()
	out := map[string]string{}
	out["interaction_empty"] = describeMessage(InteractionMessage(lang, "job.interaction_created", "  ", nil, "", 10))
	inter := InteractionMessage(lang, "job.interaction_created", "Run the tests?", []string{"yes", "no"}, "https://x/jobs/j1", 10)
	out["interaction"] = describeMessage(inter) + renderAll(t, inter, 0)
	out["approval_full"] = describeMessage(ApprovalMessage(lang, "job.awaiting_approval", "Deploy", "needs review", "go test ./...", "2026-01-02 15:04", "https://x/jobs/j1", 10))
	out["approval_empty"] = describeMessage(ApprovalMessage(lang, "job.awaiting_approval", "Deploy", "", "", "", "https://x/jobs/j1", 10))
	sess := SessionAwaitingReplyMessage(lang, "job.session_awaiting_reply", "Fix it", "pick A or B", 3, "codex", "project-a", "w-lab", "15:04", "https://x/s", 10)
	out["session_awaiting_reply"] = describeMessage(sess) + renderAll(t, sess, 0)
	out["session_awaiting_reply_bare"] = describeMessage(SessionAwaitingReplyMessage(lang, "job.session_awaiting_reply", "Fix it", "", 1, "codex", "project-a", "", "", "https://x/s", 10))
	// Default link label (the message names none) and both truncation markers.
	plain := Message{EventType: "x.y", Title: "T", Text: "abcdefghijklmnopqrstuvwxyz", Link: "https://x/y", Lang: lang}
	out["default_link_label_and_clamp"] = renderAll(t, plain, 5)
	long := Message{EventType: "x.y", Title: "T", Text: strings.Repeat("a", 30000), Link: "https://x/y", Lang: lang}
	var tail strings.Builder
	for _, kind := range []string{KindDingTalk, KindFeishu} {
		raw, err := RenderMessageWithLimit(kind, long, "", 100000)
		if err != nil {
			t.Fatal(err)
		}
		r := []rune(bodyText(t, kind, raw))
		fmt.Fprintf(&tail, "--- %s ---\n%s\n", kind, string(r[len(r)-60:]))
	}
	out["fit_truncation_tail"] = tail.String()
	return out
}

func TestNotifyTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) {
			update := os.Getenv("GOFER_GOLDEN_UPDATE") != ""
			for name, got := range notifyTexts(t, lang) {
				file := filepath.Join("testdata", "i18n_golden", name+"."+string(lang)+".txt")
				raw, err := os.ReadFile(file)
				if err != nil && lang == i18n.ZhCN {
					t.Fatalf("%s: the zh-CN golden is the recorded original and must exist: %v", file, err)
				}
				if err != nil || (update && lang != i18n.ZhCN) {
					if werr := os.WriteFile(file, []byte(got), 0o644); werr != nil {
						t.Fatal(werr)
					}
					if err != nil {
						t.Errorf("%s did not exist: written with the current text, review and commit it", file)
					}
					continue
				}
				if got != string(raw) {
					t.Errorf("%s differs from its golden file\n--- got ---\n%s\n--- want ---\n%s", file, got, raw)
				}
				if lang == i18n.EN && notifyCJK.MatchString(got) {
					t.Errorf("%s: the English text carries CJK:\n%s", name, got)
				}
			}
		})
	}
}
