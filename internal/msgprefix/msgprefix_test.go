package msgprefix

import (
	"strings"
	"testing"
)

// TestEveryKindRoundTrips pins the table itself: every kind's Format output is
// recognised again as that kind, and as the right half (person vs gofer).
func TestEveryKindRoundTrips(t *testing.T) {
	cases := []struct {
		kind   string
		user   string
		body   string
		person bool
		want   string
	}{
		{WebMessage, "alice", "进度如何", true, "[来自 web，alice] 进度如何"},
		{WebReply, "alice", "用方案 B", true, "[来自 web 回复，alice] 用方案 B"},
		{JobDone, "", "job-1 t status=done exit=0", false, "[gofer · job 完成] job-1 t status=done exit=0"},
		{Wakeup, "", "看看 CI", false, "[gofer · 唤醒] 看看 CI"},
		{WorkHandoff, "", "工作项 wr-1", false, "[gofer · 工作项交接] 工作项 wr-1"},
		{WorkReport, "", "工作项 wr-1", false, "[gofer · 工作项汇报] 工作项 wr-1"},
		{Steward, "", "资源到了", false, "[gofer · 管家带话] 资源到了"},
		{Memory, "", "因“发版”命中", false, "[gofer · 记忆] 因“发版”命中"},
		{Hint, "", "本会话还没有话题", false, "[gofer · 提示] 本会话还没有话题"},
	}
	for _, c := range cases {
		got := Format(c.kind, c.user, c.body)
		if got != c.want {
			t.Fatalf("Format(%q) = %q, want %q", c.kind, got, c.want)
		}
		kind, person := Classify(got)
		if kind != c.kind || person != c.person {
			t.Fatalf("Classify(%q) = (%q, %v), want (%q, %v)", got, kind, person, c.kind, c.person)
		}
	}
}

// TestPersonAndSystemNeverMix is the rule the two halves exist for.
func TestPersonAndSystemNeverMix(t *testing.T) {
	for _, body := range []string{"hi", "好的"} {
		for _, kind := range []string{WebMessage, WebReply} {
			text := Format(kind, "alice", body)
			if !IsPerson(text) || IsSystem(text) {
				t.Fatalf("person message %q: IsPerson=%v IsSystem=%v", text, IsPerson(text), IsSystem(text))
			}
		}
	}
	for _, p := range SystemPrefixes() {
		text := p + " job-1 status=done"
		if IsPerson(text) || !IsSystem(text) {
			t.Fatalf("system message %q: IsPerson=%v IsSystem=%v", text, IsPerson(text), IsSystem(text))
		}
	}
	// A plain human prompt carries no prefix at all: never a notice, never a
	// person's web line.
	for _, plain := range []string{"please continue", "<task-notification>x</task-notification>", ""} {
		if IsPerson(plain) || IsSystem(plain) {
			t.Fatalf("plain prompt %q was classified as a gofer message", plain)
		}
	}
}

// TestPersonMessageAlwaysNamesTheSpeaker: an unknown speaker falls back to
// DefaultUser rather than dropping the bracket (the name is what keeps the prefix
// parseable).
func TestPersonMessageAlwaysNamesTheSpeaker(t *testing.T) {
	got := Format(WebReply, "  ", "继续")
	if got != "[来自 web 回复，web] 继续" {
		t.Fatalf("Format with no user = %q", got)
	}
	if kind, person := Classify(got); kind != WebReply || !person {
		t.Fatalf("Classify(%q) = (%q, %v)", got, kind, person)
	}
}

// TestUnknownSystemKindStaysSystem: a newer (or older) build's type is still
// gofer's own message, it just is not one of the known kinds.
func TestUnknownSystemKindStaysSystem(t *testing.T) {
	text := "[gofer · 未来的类型] x"
	if kind, person := Classify(text); kind != "" || person {
		t.Fatalf("Classify(%q) = (%q, %v)", text, kind, person)
	}
	if !IsSystem(text) {
		t.Fatalf("%q should still read as a system message", text)
	}
}

// TestLegacyPrefixesAreStillClassified: sessions hold messages written before the
// table, so the recognisers keep reading the old spellings (DEPRECATED(v0.185):
// remove in v0.188).
func TestLegacyPrefixesAreStillClassified(t *testing.T) {
	cases := []struct {
		text   string
		kind   string
		person bool
	}{
		{"[gofer web 回复] carry on", WebReply, true},
		{"[gofer 通知] [gofer job 完成] job-1 t status=done exit=0", JobDone, false},
		{"[gofer job 完成] job-1 t status=done exit=0", JobDone, false},
		{"[gofer wakeup] check the CI", Wakeup, false},
		{"[gofer 工作项交接请求 wrq-1] 工作项 wr-1", WorkHandoff, false},
		{"[gofer 工作项汇报请求 wrq-2] 请汇报", WorkReport, false},
		{"[gofer 管家带话] 资源到了", Steward, false},
		{"[gofer 记忆 · 因“发版”命中] release-flow", Memory, false},
		{"[gofer] This session has no topic recorded yet.", Hint, false},
	}
	for _, c := range cases {
		kind, person := Classify(c.text)
		if kind != c.kind || person != c.person {
			t.Fatalf("Classify(%q) = (%q, %v), want (%q, %v)", c.text, kind, person, c.kind, c.person)
		}
		if person != IsPerson(c.text) || person == IsSystem(c.text) {
			t.Fatalf("%q: IsPerson=%v IsSystem=%v, want person=%v", c.text, IsPerson(c.text), IsSystem(c.text), person)
		}
	}
}

// TestFormatIsTrimSpaceTolerant: deliveries may carry leading whitespace (a line
// built by concatenation); the prefix still reads.
func TestFormatIsTrimSpaceTolerant(t *testing.T) {
	text := Format(JobDone, "", "job-1 done")
	if kind, _ := Classify("\n\t " + text + " "); kind != JobDone {
		t.Fatalf("Classify with padding did not see %q", kind)
	}
	if !strings.HasPrefix(Prefix(JobDone, ""), systemOpen) {
		t.Fatalf("system prefix %q does not open with %q", Prefix(JobDone, ""), systemOpen)
	}
}
