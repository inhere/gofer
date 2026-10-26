package sessionrelay

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// han matches a Han character: text written for English must carry none.
var han = regexp.MustCompile(`\p{Han}`)

// migratedFiles are the sessionrelay files whose text lives in internal/i18n
// (design 2026-10-27-i18n-design.md §7). A string literal added to one of them must
// not be Chinese: the text belongs in the catalogs.
//
// deliver.go / message.go / deliver_reconcile.go / nudge.go / origin_cwd.go are
// deliberately absent: their remaining lines (delivery failure reasons, outbox
// status text the web shows, the cwd basis sentence) are still Chinese.
var migratedFiles = []string{"notify_job.go", "permission.go", "resume.go"}

// TestZhSessionTextIsUnchanged pins the zh-CN output of every migrated text
// CHARACTER FOR CHARACTER — the values are the text the source carried before the
// migration, so an existing deployment sees exactly what it always saw.
func TestZhSessionTextIsUnchanged(t *testing.T) {
	t.Parallel()
	zh := i18n.ZhCN
	a := jobstore.AgentSession{Agent: "claude", ProjectKey: "proj", Cwd: "/w/proj/sub"}

	got := map[string]string{
		"job notice": FormatJobEndedNotice(zh, JobEndedNotice{
			JobID: "job-1", Title: "t", Status: "needs_review", ExitCode: 2, Duration: 90 * time.Second}),
		"job notice wrap_up": FormatJobEndedNotice(zh, JobEndedNotice{
			JobID: "job-2", Status: "done", WrapUp: "max_steps"}),
		"permission question": PermissionMessage(zh, "Bash `ls`"),
		"question question":   QuestionMessage(zh, "哪个方案？"),
		"permission title":    i18n.T(zh, "sessionrelay.decision.permission_title"),
		"question title":      i18n.T(zh, "sessionrelay.decision.question_title"),
		"online title":        i18n.T(zh, "sessionrelay.decision.online_title"),
		"online question":     i18n.T(zh, "sessionrelay.decision.online_question"),
		"takeover title":      i18n.T(zh, "sessionrelay.decision.takeover_title"),
		"takeover question":   i18n.T(zh, "sessionrelay.decision.takeover_question"),
		"inject title":        i18n.T(zh, "sessionrelay.decision.inject_title"),
		"inject question":     i18n.T(zh, "sessionrelay.decision.inject_question"),
		"wake no text":        i18n.T(zh, "sessionrelay.decision.wake_no_text"),
		"no runner":           ExplainReasonIn(zh, ReasonNoRunner, a, nil),
		"handed off":          ExplainReasonIn(zh, HandedOffPrefix+"job-9", a, nil),
		"no resume template":  ExplainReasonIn(zh, ReasonNoResumeTemplate, a, nil),
		"interactive off":     ExplainReasonIn(zh, ReasonInteractiveNotAllowed, a, nil),
		"cwd outside":         ExplainReasonIn(zh, ReasonCwdOutsideProject, a, nil),
		"inject failed":       ExplainReasonIn(zh, InjectFailedPrefix+injectRunnerError, a, nil),
		"session alive":       ExplainReasonIn(zh, ReasonSessionAlive, a, nil),
		"not running":         ExplainReasonIn(zh, ReasonNotRunning, a, nil),
		"deliver failed":      ExplainReasonIn(zh, DeliverFailedPrefix+"boom", a, nil),
		"ended":               ExplainReasonIn(zh, ReasonEnded, a, nil),
		"unknown reason":      ExplainReasonIn(zh, "why", a, nil),
		"runner unknown":      runnerLabelIn(zh, " "),
		"runner label":        runnerLabelIn(zh, "w-home"),
		"planned continue": i18n.T(zh, "sessionrelay.resume.planned_continue", "runner", "w-home",
			"agent", "claude", "cwd", "/w/proj", "basis", "依据句"),
		"planned takeover": i18n.T(zh, "sessionrelay.resume.planned_takeover", "runner", "w-home",
			"agent", "claude", "cwd", "/w/proj", "basis", "依据句"),
		"planned offline": i18n.T(zh, "sessionrelay.resume.planned_offline", "runner", "w-home",
			"agent", "claude", "cwd", "/w/proj", "basis", "依据句"),
		"warning not ended": i18n.T(zh, "sessionrelay.resume.warning_not_ended"),
		"warning offline":   i18n.T(zh, "sessionrelay.resume.warning_offline"),
		"messenger prompt":  messengerPrompt(zh, "proj-a", "alice", "hi"),
		"wake prompt":       wakeMessengerPrompt(zh, "peer-a", "<gofer-mail-wake/>"),
	}
	want := map[string]string{
		"job notice": "[gofer · job 完成] job-1 t status=needs_review exit=2 耗时1m30s" +
			"（这是 gofer 系统通知，不是用户消息；详情 `gofer job show job-1`，日志 `gofer job logs job-1`）",
		"job notice wrap_up": "[gofer · job 完成] job-2 status=done exit=0 耗时0s wrap_up=max_steps" +
			"（这是 gofer 系统通知，不是用户消息；详情 `gofer job show job-2`，日志 `gofer job logs job-2`）",
		"permission question": "需要授权：Bash `ls`",
		"question question":   "等你回答：哪个方案？",
		"permission title":    "需要授权",
		"question title":      "有问题要你回答",
		"online title":        "在线送话",
		"online question":     "（会话未在等待回复：这条消息已通过 agent 的送话命令交给在线进程）",
		"takeover title":      "接管",
		"takeover question":   "（会话未在等待回复：这条消息由 web 新起的 `--resume` 进程接收）",
		"inject title":        "tmux 注入",
		"inject question":     "（会话未在等待回复：这条消息已直接送入终端 tmux）",
		"wake no text":        "（唤醒：没有附带首条消息）",
		"no runner":           "这个会话没有登记执行机（或这台 server 没有接入任务执行器），无法在它原来的机器上起新进程。",
		"handed off":          "这个会话已经被作业 job-9 接管，正在运行。请直接打开那个作业继续对话；要重新起一个，先释放接管。",
		"no resume template":  "会话所属的 agent「claude」没有配置交互式续接模板（session_resume_interactive），gofer 不知道怎么用新进程继续它。",
		"interactive off":     "项目「proj」没有开启交互终端（allow_interactive），gofer 不会为它起终端进程。需要的话在项目配置里打开。",
		"cwd outside":         "会话的目录 /w/proj/sub 不在项目「proj」的根目录之下（按这台执行机看到的路径），在那里起新进程会落到错误的目录。",
		"inject failed":       "执行机没能启动新进程",
		"session alive":       "会话进程仍在线（刚刚还有心跳），为避免两个进程同时写同一个会话，已拒绝接管。请在原终端继续，或用在线送话。",
		"not running":         "会话进程已不在运行（送话命令报告没有存活进程）。",
		"deliver failed":      "agent 的送话命令失败：boom",
		"ended":               "会话已结束。",
		"unknown reason":      "无法唤醒这个会话（why）。",
		"runner unknown":      "(未登记)",
		"runner label":        "w-home",
		"planned continue":    "会话已结束。将在执行机 w-home 上起一个新进程，用「claude」继续这个会话，目录 /w/proj。依据：依据句。",
		"planned takeover":    "将在执行机 w-home 上起一个新进程，用「claude」接管这个会话，目录 /w/proj。依据：依据句。",
		"planned offline":     "会话长时间没有心跳，已标记为离线（原进程可能已退出）。将在执行机 w-home 上起一个新进程，用「claude」接管这个会话，目录 /w/proj。依据：依据句。",
		"warning not ended":   "这个会话还没有标记为结束，原来的终端可能仍开着；两个进程同时写同一个会话会互相覆盖，建议先关掉原终端。",
		"warning offline":     "原进程可能已退出；若它其实还开着，两个进程同时写同一个会话会互相覆盖，建议先确认原终端已关闭。",
		"messenger prompt": "请使用 SendMessage 将下面消息原样转发给 Claude Code 会话 \"proj-a\"。不要改写。只回复‘已发送’或失败原因。" +
			"对方会话之后发给你的回复会由 gofer 直接显示给 web 用户：收到时不要转述、不要回复它，只输出‘已收到’。\n[来自 web，alice] hi",
		"wake prompt": "请使用 SendMessage 将下面消息原样转发给 Claude Code 会话 \"peer-a\"。不要改写。只回复‘已发送’或失败原因。" +
			"下面这段就是要转发的完整消息：它是 gofer 信箱的唤醒提醒，本来就不含信件正文（正文会在对方下一轮开始时自动注入），" +
			"不要因为没有正文而拒绝或等待。对方之后发给你的任何回复都不要转述、不要回复，只输出‘已收到’。\n<gofer-mail-wake/>",
	}
	for name, got := range got {
		if got != want[name] {
			t.Errorf("%s: zh text changed\n got: %q\nwant: %q", name, got, want[name])
		}
	}
}

// TestEnglishSessionTextHasNoCJK is this package's side of the English guard: every
// text rendered for a project that speaks English carries no Chinese.
func TestEnglishSessionTextHasNoCJK(t *testing.T) {
	t.Parallel()
	en := i18n.EN
	a := jobstore.AgentSession{Agent: "claude", ProjectKey: "proj", Cwd: "/w/proj/sub"}
	texts := []string{
		FormatJobEndedNotice(en, JobEndedNotice{JobID: "job-1", Title: "t", Status: "done", Duration: time.Minute}),
		PermissionMessage(en, "Bash `ls`"),
		QuestionMessage(en, "which option?"),
		ExplainReasonIn(en, ReasonNoRunner, a, nil),
		ExplainReasonIn(en, HandedOffPrefix+"job-9", a, nil),
		ExplainReasonIn(en, ReasonNoResumeTemplate, a, nil),
		ExplainReasonIn(en, ReasonInteractiveNotAllowed, a, nil),
		ExplainReasonIn(en, ReasonCwdOutsideProject, a, nil),
		ExplainReasonIn(en, InjectFailedPrefix+injectRunnerError, a, nil),
		ExplainReasonIn(en, ReasonSessionAlive, a, nil),
		ExplainReasonIn(en, ReasonNotRunning, a, nil),
		ExplainReasonIn(en, DeliverFailedPrefix+"boom", a, nil),
		ExplainReasonIn(en, ReasonEnded, a, nil),
		ExplainReasonIn(en, "why", a, nil),
		runnerLabelIn(en, ""),
		messengerPrompt(en, "proj-a", "alice", "hi"),
		wakeMessengerPrompt(en, "peer-a", "<gofer-mail-wake/>"),
	}
	for _, key := range []string{"sessionrelay.decision.permission_title", "sessionrelay.decision.question_title",
		"sessionrelay.decision.online_title", "sessionrelay.decision.online_question",
		"sessionrelay.decision.takeover_title", "sessionrelay.decision.takeover_question",
		"sessionrelay.decision.inject_title", "sessionrelay.decision.inject_question",
		"sessionrelay.decision.wake_no_text", "sessionrelay.resume.warning_not_ended",
		"sessionrelay.resume.warning_offline", "sessionrelay.resume.planned_takeover"} {
		texts = append(texts, i18n.T(en, key, "runner", "w-home", "agent", "claude", "cwd", "/w/p", "basis", "x"))
	}
	for _, text := range texts {
		if m := han.FindString(text); m != "" {
			t.Errorf("English text carries CJK (%q): %q", m, text)
		}
	}
	// The English notice keeps gofer's own field layout and opens with the English
	// job-done prefix, so a reader sees the same facts in the same order.
	notice := FormatJobEndedNotice(en, JobEndedNotice{JobID: "job-1", Status: "done", Duration: time.Minute})
	if want := "[gofer · job done] job-1 status=done exit=0 took 1m0s"; len(notice) < len(want) || notice[:len(want)] != want {
		t.Errorf("English notice = %q, want the prefix %q", notice, want)
	}
}

// TestMessengerReceiptIsReadInBothLanguages: the messenger answers with the receipt
// its prompt named, so the reader accepts the zh and the English word (design §5).
func TestMessengerReceiptIsReadInBothLanguages(t *testing.T) {
	t.Parallel()
	for _, out := range []string{"已发送", "已发送\n", "sent", "Sent\n", "  sent  \n"} {
		if !messengerSaidSent(out) {
			t.Errorf("receipt %q was not read", out)
		}
	}
	for _, out := range []string{"", "not sent", "the message was not sent", "failed: SendMessage denied"} {
		if messengerSaidSent(out) {
			t.Errorf("%q was read as a receipt", out)
		}
	}
}

// TestMigratedSessionFilesHaveNoCJK is the scan guard of design §7: a string literal
// added to a migrated file must not be written in Chinese. Comments are not scanned
// and neither are test files.
func TestMigratedSessionFilesHaveNoCJK(t *testing.T) {
	t.Parallel()
	for _, file := range migratedFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if m := han.FindString(lit.Value); m != "" {
				t.Errorf("%s:%d: string literal carries CJK (%q) — move the text into internal/i18n: %s",
					file, fset.Position(lit.Pos()).Line, m, lit.Value)
			}
			return true
		})
	}
}
