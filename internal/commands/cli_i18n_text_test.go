package commands

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// han matches a Han character: a help text or message rendered for English must
// carry none (design 2026-10-27-i18n-design.md §7).
var han = regexp.MustCompile(`\p{Han}`)

// TestEnglishCliHelpHasNoCJK: with GOFER_LANG=en the help of the migrated
// subtrees is English — no Chinese word survives anywhere in it.
func TestEnglishCliHelpHasNoCJK(t *testing.T) {
	t.Setenv("GOFER_LANG", string(i18n.EN))
	for _, roots := range [][]string{migratedHelpRoots, setupHelpRoots} {
		if m := han.FindString(collectCliHelpFor(t, roots)); m != "" {
			t.Errorf("the English help carries CJK (%q):\n%s", m, collectCliHelpFor(t, roots))
		}
	}
}

// TestCliLangChain pins the language chain of the CLI's own output (design §3):
// GOFER_LANG, then the machine's locale, then the default. The client node's
// configured language sits between the last two in §3 and is not wired yet (see
// cliLang); its test comes with the setup/config group that owns the client .env.
func TestCliLangChain(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8") // the machine: English
	t.Setenv("LANG", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv(i18n.EnvLanguageVar, "")

	if got := cliLang(); got != i18n.EN {
		t.Errorf("machine locale only: cliLang() = %q, want en", got)
	}
	// A regional / encoded spelling of the machine's language reads canonical.
	t.Setenv("LC_ALL", "zh_CN.UTF-8")
	if got := cliLang(); got != i18n.ZhCN {
		t.Errorf("machine locale zh_CN.UTF-8: cliLang() = %q, want zh-CN", got)
	}
	// GOFER_LANG beats the machine locale.
	t.Setenv(i18n.EnvLanguageVar, "en")
	if got := cliLang(); got != i18n.EN {
		t.Errorf("GOFER_LANG: cliLang() = %q, want en", got)
	}
	// Nothing names a language: the DEPRECATED default.
	t.Setenv(i18n.EnvLanguageVar, "")
	t.Setenv("LC_ALL", "sn")
	if got := cliLang(); got != i18n.Default {
		t.Errorf("nothing set: cliLang() = %q, want the default %q", got, i18n.Default)
	}
}

// TestZhCliMessagesAreUnchanged pins the zh-CN text of the messages that are not
// part of --help (they are printed by a command that needs a server). The
// expected strings were copied from the code BEFORE the migration, so a change to
// the Chinese output of an existing deployment turns this red.
func TestZhCliMessagesAreUnchanged(t *testing.T) {
	cases := []struct {
		key  string
		want string
		args []any
	}{
		{"cli.job.err.no_config", "未找到配置文件，且未通过 -s/--server 指定 server 地址；" +
			"请用 -s/--server 指定，或配置 $GOFER_CONFIG_DIR/.env（或 --config 指定配置文件）", nil},
		{"cli.job.err.approval_needs_person", "批准必须由人在 web 或 agent 会话之外的终端操作 (approval must be given by a person in the web console or a terminal outside any agent session)", nil},
		{"cli.job.err.notify_no_session", "--notify 没有可通知的会话：用 --notify-session <sid> 指定，或从 agent 会话里提交（$GOFER_SESSION_ID / $CLAUDE_CODE_SESSION_ID；会话列表见 gofer session ls）", nil},
		{"cli.job.err.no_tracker_id", "无法从 job cwd 找到 tracker_id；请在工作目录或其父目录初始化 .gofer/tracker，或显式传 --tracker-id", nil},
		{"cli.job.warn.hold_notify_rejected", "warning: 默认的 hold 结束通知被服务端拒绝（boom），本次不通知；需要通知请用 --notify-session <sid> 指定", []any{"err", "boom"}},
		{"cli.job.warn.secret_in_command", "warning: 命令行疑似包含秘密，会被保存在 job 记录中（位置：cmd --token）", []any{"locations", "cmd --token"}},
		{"cli.job.run.notify", "notify: job 结束时通知会话 ab12", []any{"session", "ab12"}},
		{"cli.job.resume.notify", "notify: job 结束时通知会话 ab12（--no-notify 关掉）", []any{"session", "ab12"}},
		{"cli.job.show.queued_wait", "排队:       12s", []any{"duration", "12s"}},
		{"cli.job.show.waiting_dir_help", "waiting_dir_help: 被占用；只读任务加 --read-only，或用 --lock 收窄范围、--shared-dir 放弃独占、--worktree 隔离", nil},
		{"cli.job.review.out_of_scope", "out_of_scope: 3 file(s) outside the declared scope (范围外)", []any{"count", 3}},
		{"cli.job.review.acceptance", "验收标准 (acceptance):", nil},
		{"cli.job.review.findings", "发现但不碰 (2; file them with `job findings j1 --create-issues`):", []any{"count", 2, "id", "j1"}},
		{"cli.job.review.hints_head", "审阅提示 (review hints):", nil},
		{"cli.job.review.hotspots", "deletion hotspots (删除最多; net = 新增-删除, ⚠ = 删除显著多于新增):", nil},
		{"cli.job.review.removed_comments", "removed comments (被删注释):", nil},
		{"cli.job.review.verify_lines", "verify lines (汇报里的验证结果):", nil},
		{"cli.job.review.report_verify", "report verify (来源: agent 自述, 仅参考):", nil},
		{"cli.job.findings.none", "job j1 reported no 「发现但不碰」 items", []any{"id", "j1", "section", i18n.Title(i18n.ZhCN, i18n.SectionFindings)}},
		{"cli.plan.create.notify_on", "notify: 该 plan 派发的 job 结束时通知主 Agent 会话", nil},
		{"cli.plan.create.bound", "已绑定主 Agent 会话 ab12", []any{"session", "ab12"}},
		{"cli.plan.create.bound_named", "已绑定主 Agent 会话 ab12 (omp)", []any{"session", "ab12", "name", "omp"}},
		{"cli.plan.create.bound_hint", "job 结束会通知该会话；不需要时：gofer plan set p1 --clear-supervisor-session（或创建时加 --no-supervisor）", []any{"plan", "p1"}},
		{"cli.plan.create.notify_without_supervisor", "warning: notify 已开但 plan 没有主 Agent 会话，派发的 job 不会通知任何人（用 --supervisor-session-id <sid> 绑定）", nil},
		{"cli.plan.set.supervisor_changed", "主 Agent 会话 ->", nil},
		{"cli.plan.bind.none", "未绑定主 Agent 会话（not inside an agent session; 绑定：gofer plan set p1 --supervisor-session-id <sid>，见 gofer session ls）", []any{"reason", "not inside an agent session; ", "plan", "p1"}},
		{"cli.plan.list.footer", "共 57 条，显示 21–40", []any{"total", 57, "from", 21, "to", 40}},
		{"cli.plan.show.leader_switch_off", ", 总开关未开", nil},
		{"cli.session.list.hidden", "另有 4 个超过 7 天未活动的会话已隐藏，用 --all 查看", []any{"count", 4, "days", 7}},
		{"cli.session.list.maybe_offline", "(可能不在线)", nil},
		{"cli.session.inject_mode.auto_note", "messages are typed only when the input area is confirmed empty; otherwise they wait for the session's next hook (the web page sends them with 「现在送」)", nil},
		{"cli.session.relay.on_note", "every stop now waits on the gofer web 会话 page until you reply there (/off releases it)", nil},
		{"cli.session.flag.inject_send_held", "instead of setting a mode, send the messages waiting for this session right now (「现在送」)", nil},
		{"cli.plan.flag.no_supervisor", "do not bind the current agent session as the plan's supervisor (主 Agent)", nil},
	}
	// One key takes an argument that is itself localized (a protocol section title);
	// its English rendering is checked with the English title.
	enArgs := map[string][]any{
		"cli.job.findings.none": {"id", "j1", "section", i18n.Title(i18n.EN, i18n.SectionFindings)},
	}
	for _, tc := range cases {
		if got := i18n.T(i18n.ZhCN, tc.key, tc.args...); got != tc.want {
			t.Errorf("%s =\n %q\nwant\n %q", tc.key, got, tc.want)
		}
		args, ok := enArgs[tc.key]
		if !ok {
			args = tc.args
		}
		if m := han.FindString(i18n.T(i18n.EN, tc.key, args...)); m != "" {
			t.Errorf("%s: the English text carries CJK (%q)", tc.key, m)
		}
	}
}

// TestMigratedCliFilesHaveNoCJK is the scan guard of design §7: a string literal
// added to a migrated file must not be written in Chinese — the text belongs in
// internal/i18n. Comments are not scanned, and test files are out of scope.
func TestMigratedCliFilesHaveNoCJK(t *testing.T) {
	for _, file := range []string{
		"job.go", "job_findings.go", "plan.go", "session.go",
		// S2b2: the setup & config group.
		"config.go", "init_workspace.go", "project.go", "stop.go", "serve.go",
		"worker.go", "worker_doctor.go",
	} {
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

// TestCliCatalogKeysMatch: both languages carry the same keys (design §7), so a
// message that exists in one catalog cannot be missing in the other.
func TestCliCatalogKeysMatch(t *testing.T) {
	for _, domain := range []string{"cli.job", "cli.plan", "cli.session"} {
		keys := catalogKeys(t, domain)
		if len(keys) == 0 {
			t.Fatalf("%s: no keys found", domain)
		}
	}
}

// catalogKeys lists the keys of domain in both catalogs and fails when they
// differ. The keys are read through i18n.T on the values the code uses, which is
// what makes a missing English value a test failure rather than a silent
// fallback: T falls back to English, so an English value must never be "".
func catalogKeys(t *testing.T, domain string) []string {
	t.Helper()
	var out []string
	for _, lang := range i18n.Supported() {
		for _, key := range cliCatalogKeys(domain) {
			if got := i18n.T(lang, key); got == key {
				t.Errorf("%s: key %q is missing in %s", domain, key, lang)
			}
		}
	}
	return append(out, cliCatalogKeys(domain)...)
}

// cliCatalogKeys lists the keys of one domain that the migrated code uses. It is
// kept next to the tests (not the catalogs) so an unused key is visible here.
func cliCatalogKeys(domain string) []string {
	switch domain {
	case "cli.job":
		return []string{
			"cli.job.desc.review", "cli.job.desc.findings",
			"cli.job.flag.worktree_note", "cli.job.flag.no_scope_discipline", "cli.job.flag.acceptance",
			"cli.job.err.no_config", "cli.job.err.approval_needs_person", "cli.job.err.notify_no_session", "cli.job.err.no_tracker_id",
			"cli.job.warn.hold_notify_rejected", "cli.job.warn.secret_in_command",
			"cli.job.run.notify", "cli.job.resume.notify",
			"cli.job.show.queued_wait", "cli.job.show.waiting_dir_help",
			"cli.job.review.out_of_scope", "cli.job.review.acceptance", "cli.job.review.findings",
			"cli.job.review.hints_head", "cli.job.review.hotspots", "cli.job.review.removed_comments",
			"cli.job.review.verify_lines", "cli.job.review.report_verify",
			"cli.job.findings.none",
		}
	case "cli.plan":
		return []string{
			"cli.plan.flag.no_supervisor",
			"cli.plan.create.notify_on", "cli.plan.create.bound", "cli.plan.create.bound_named",
			"cli.plan.create.bound_hint", "cli.plan.create.notify_without_supervisor",
			"cli.plan.set.supervisor_changed", "cli.plan.bind.none",
			"cli.plan.list.footer", "cli.plan.show.leader_switch_off",
		}
	case "cli.session":
		return []string{
			"cli.session.flag.inject_send_held", "cli.session.list.hidden",
			"cli.session.list.maybe_offline", "cli.session.inject_mode.auto_note",
			"cli.session.relay.on_note",
		}
	}
	return nil
}

// TestZhHelpDescriptionKeysAreTranslated guards the two descriptions that used to
// mix an English sentence with a Chinese term: the en catalog must read as plain
// English while zh-CN keeps its established wording.
func TestZhHelpDescriptionKeysAreTranslated(t *testing.T) {
	if got := i18n.T(i18n.EN, "cli.job.desc.findings"); !strings.Contains(got, "Out-of-scope findings") {
		t.Errorf("en findings description = %q", got)
	}
	if got := i18n.T(i18n.ZhCN, "cli.job.desc.findings"); !strings.Contains(got, "发现但不碰") {
		t.Errorf("zh findings description = %q", got)
	}
}
