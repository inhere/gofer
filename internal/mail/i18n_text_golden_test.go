package mail

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The golden files under testdata/i18n_golden pin the text the mailbox writes for an
// agent (the resident session's opening prompt) or a person (bounce / hold reasons).
// The *.zh-CN.txt files were recorded from the code BEFORE it was moved into the
// catalog; the *.en.txt files are the English rewrite. GOFER_GOLDEN_UPDATE=1 rewrites
// the English ones after a deliberate edit.

var mailTextCJK = regexp.MustCompile(`[\p{Han}]`)

func mailTextOutputs(lang i18n.Lang) map[string]string {
	out := map[string]string{}
	spec := ResidentAgentSpec{Name: "helper", Idle: 30 * time.Minute, Prompt: "  be nice  "}
	out["opening_project"] = OpeningPrompt(lang, spec, "proj-a")
	spec.Prompt = ""
	out["opening_plain"] = OpeningPrompt(lang, spec, "")
	out["limit"] = residentLimitReason(lang, 3)
	now := time.Unix(1_000_000, 0)
	out["rotate_letters"] = rotationReason(lang, jobstore.MailResidentAgent{Letters: 5}, ResidentAgentSpec{MaxLetters: 5}, now)
	out["rotate_hours"] = rotationReason(lang, jobstore.MailResidentAgent{CreatedAt: now.Add(-3 * time.Hour).Unix()}, ResidentAgentSpec{MaxHours: 2}, now)
	out["capture_where"] = captureWhere(lang, "") + "\n" + captureWhere(lang, "item-1")
	out["bounce_reasons"] = bounceReason(lang, PtyHoldReason) + "\n" + bounceReason(lang, noInboxReason(lang)) + "\n" + bounceReason(lang, "custom") +
		"\n" + overdueReasonText(lang) + "\n" + oneOffJobReason(lang)
	out["bounce_text"] = BounceTextLang(lang, jobstore.Mail{ID: 7, ToAddress: "agent:x", Urgent: true, Body: "hello body"}, "reason x", "where x")
	return out
}

func TestMailTextGolden(t *testing.T) {
	for _, lang := range i18n.Supported() {
		t.Run(string(lang), func(t *testing.T) {
			update := os.Getenv("GOFER_GOLDEN_UPDATE") != ""
			for name, got := range mailTextOutputs(lang) {
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
				if lang == i18n.EN && mailTextCJK.MatchString(got) {
					t.Errorf("%s: the English text carries CJK:\n%s", name, got)
				}
			}
		})
	}
}

// TestStoredReasonsAreRecognizedInEveryLanguage: a hold reason is stored in the language
// of the moment, so a bounce written later (another language, a config change) still
// turns the machine wording into the sentence.
func TestStoredReasonsAreRecognizedInEveryLanguage(t *testing.T) {
	for _, stored := range i18n.Supported() {
		for _, now := range i18n.Supported() {
			got := bounceReason(now, noInboxReason(stored))
			if got != noInboxReason(now) {
				t.Errorf("reason stored in %s, read in %s = %q, want %q", stored, now, got, noInboxReason(now))
			}
		}
	}
	if got := bounceReason(i18n.EN, "  free text "); got != "free text" {
		t.Errorf("free text = %q", got)
	}
	if !strings.Contains(bounceReason(i18n.EN, PtyHoldReason), "pty") {
		t.Errorf("pty reason = %q", bounceReason(i18n.EN, PtyHoldReason))
	}
}
