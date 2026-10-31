package sessionrelay

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// TestChooseResumeCwdReasonText pins the basis sentence of the takeover plan
// (CwdChoice.Reason, the "依据：…" of the plan message). The zh-CN strings are what
// the code wrote BEFORE the sentence moved into the catalog (recorded from the old
// code), so zh-CN output is unchanged character for character; the en strings are
// the English rewrite and must carry no Han character.
func TestChooseResumeCwdReasonText(t *testing.T) {
	t.Parallel()
	tr := `/home/u/.claude/projects/-w-proj/sid.jsonl`
	plan := func(dirs ...string) TakeoverPlan {
		if len(dirs) == 0 {
			return TakeoverPlan{ExecRoot: "/w/proj"}
		}
		return TakeoverPlan{ExecRoot: "/w/proj", DirExists: existsSet(dirs...)}
	}
	cases := []struct {
		name string
		cwd  string
		tr   string
		plan TakeoverPlan
		zh   string
		en   string
	}{
		{"transcript, runner cannot be asked", "/w/proj", tr, plan(),
			"会话文件所在目录名验证了会话启动时的目录（执行机在远端，未核对该目录是否存在）",
			"the session file's directory name confirms the directory the session started in (the execution machine is remote: this directory was not checked)"},
		{"transcript confirmed, registered cwd differs", "/w/proj/tmp", tr, plan("/w/proj"),
			"会话文件所在目录名验证了会话启动时的目录（已确认存在）；登记的目录与它不同（可能是中途换的临时目录），已忽略",
			"the session file's directory name confirms the directory the session started in (confirmed to exist); the registered directory differs (probably a temporary one picked up mid-way) and was ignored"},
		{"no transcript, registered used", "/w/proj/sub", "", plan("/w/proj", "/w/proj/sub"),
			"会话没有登记会话文件路径，无法验证原始启动目录，改用登记的目录（已确认存在）",
			"the session registered no session-file path, so the original start directory cannot be confirmed, using the registered directory (confirmed to exist)"},
		{"transcript matches nothing", "/w/proj/sub", "/home/u/.claude/projects/-other/sid.jsonl", plan(),
			"会话文件目录名与登记目录、项目根都对不上，无法验证原始启动目录，改用登记的目录（执行机在远端，未核对该目录是否存在）",
			"the session file's directory name matches neither the registered directory nor the project root, so the original start directory cannot be confirmed, using the registered directory (the execution machine is remote: this directory was not checked)"},
		{"registered gone, project root", "/w/proj/gone", "", plan("/w/proj"),
			"会话没有登记会话文件路径，无法验证原始启动目录，且登记的目录在执行机上已不存在，回落到项目根",
			"the session registered no session-file path, so the original start directory cannot be confirmed, and the registered directory no longer exists on the execution machine, so gofer falls back to the project root"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			a := sess(c.cwd, c.tr)
			for lang, want := range map[i18n.Lang]string{i18n.ZhCN: c.zh, i18n.EN: c.en} {
				got, ok := ChooseResumeCwdIn(lang, a, c.plan)
				if !ok {
					t.Fatalf("%s: no choice", lang)
				}
				if got.Reason != want {
					t.Errorf("%s reason\n got: %q\nwant: %q", lang, got.Reason, want)
				}
			}
			if got, _ := ChooseResumeCwdIn(i18n.EN, a, c.plan); han.MatchString(got.Reason) {
				t.Errorf("en reason carries Han: %q", got.Reason)
			}
			// The language-less form is the default language (zh-CN).
			if got, _ := ChooseResumeCwd(a, c.plan); got.Reason != c.zh {
				t.Errorf("ChooseResumeCwd default = %q, want %q", got.Reason, c.zh)
			}
		})
	}
}
