package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/workbench"
	"github.com/inhere/gofer/internal/wsproto"
)

var errKeyCJK = regexp.MustCompile(`[\p{Han}]`)

// getWithLang is GET path with an Accept-Language header, decoded as an error body.
func getErrWithLang(t *testing.T, s *Server, path, acceptLanguage string) (int, errorBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body errorBody
	decode(t, rec.Result(), &body)
	return rec.Code, body
}

// TestKeyedErrorBodyFollowsRequestLanguage: a keyed error carries a stable `code`, the
// placeholder values as `params`, and a `detail` in the request language. The Chinese
// detail and the Chinese-era `error` phrase are byte-identical to what the handler
// wrote before it was migrated; English carries no CJK anywhere.
func TestKeyedErrorBodyFollowsRequestLanguage(t *testing.T) {
	runnersCfg := map[string]config.RunnerConfig{
		"old": {Type: "worker", WorkerID: "w-old"},
		"off": {Type: "worker", WorkerID: "w-off"},
	}
	have := wsproto.MessengerListMinProtocolVersion - 1
	workers := fakeWorkers{"w-old": {Connected: true, ProtocolVersion: have, Projects: []string{"p"}}}
	s := newRunnersServer(t, runnersCfg, nil, workers)
	s.residentMessenger = &fakeResident{}

	cases := []struct {
		name, path string
		status     int
		code       string
		zhError    string
		zhDetail   string
		params     map[string]any
	}{
		{"worker too old", "/v1/runners/old/messenger/agents", http.StatusConflict, "messenger_worker_too_old", "worker 版本过旧",
			fmt.Sprintf("worker w-old 的协议版本是 v%d，列出会话需要 v%d 及以上；请先升级该 worker（gofer worker upgrade）", have, wsproto.MessengerListMinProtocolVersion),
			map[string]any{"worker": "w-old", "have": float64(have), "need": float64(wsproto.MessengerListMinProtocolVersion)}},
		{"worker offline", "/v1/runners/off/messenger/agents", http.StatusConflict, "messenger_worker_offline", "worker 不在线",
			"worker w-off 当前没有连接，无法列出它的会话", map[string]any{"worker": "w-off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, zh := getErrWithLang(t, s, tc.path, "zh-CN")
			if status != tc.status || zh.Code != tc.code || zh.Error != tc.zhError || zh.Detail != tc.zhDetail {
				t.Errorf("zh-CN = %d %+v, want %d code=%s error=%q detail=%q", status, zh, tc.status, tc.code, tc.zhError, tc.zhDetail)
			}
			for k, want := range tc.params {
				if zh.Params[k] != want {
					t.Errorf("params[%s] = %v, want %v", k, zh.Params[k], want)
				}
			}
			status, en := getErrWithLang(t, s, tc.path, "en")
			if status != tc.status || en.Code != tc.code {
				t.Errorf("en = %d code=%q, want %d code=%q", status, en.Code, tc.status, tc.code)
			}
			if errKeyCJK.MatchString(en.Error) || errKeyCJK.MatchString(en.Detail) || en.Detail == "" {
				t.Errorf("en error=%q detail=%q: want English text", en.Error, en.Detail)
			}
			// No header: the server's language (zh-CN here is the default).
			if _, def := getErrWithLang(t, s, tc.path, ""); def.Detail != tc.zhDetail {
				t.Errorf("no header detail = %q, want the server-language text", def.Detail)
			}
		})
	}
}

// TestUnkeyedErrorKeepsItsShape: an error without a code does not grow `code` / `params`
// (the body of every other error stays what scripts already parse).
func TestUnkeyedErrorKeepsItsShape(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(errorBody{Error: "x", Detail: "y"})
	if err != nil || string(b) != `{"error":"x","detail":"y"}` {
		t.Errorf("unkeyed body = %s (%v)", b, err)
	}
	e := newAPIError(i18n.EN, 409, "m", "workbench_turn_ended")
	b, _ = json.Marshal(errorBody{Error: e.Msg, Detail: e.Detail, Code: e.Code, Params: e.Params})
	if !strings.Contains(string(b), `"code":"workbench_turn_ended"`) || strings.Contains(string(b), "params") {
		t.Errorf("keyed body without args = %s", b)
	}
}

// TestWorkbenchTurnErrorSentences pins the Chinese sentences of a failed workbench turn
// (composed from reason texts before the migration) and gives every reason an English
// counterpart.
func TestWorkbenchTurnErrorSentences(t *testing.T) {
	t.Parallel()
	undeliverable := func(reason string) error {
		return &sessionrelay.UndeliverableError{Reason: reason, Err: errors.New("e")}
	}
	cases := []struct {
		name string
		err  error
		code string
		zh   string
	}{
		{"no runner", undeliverable(sessionrelay.ReasonNoRunner), "workbench_turn_undeliverable",
			"会话当前无法接收消息：会话没有登记执行机（" + undeliverable(sessionrelay.ReasonNoRunner).Error() + "）"},
		{"ended", undeliverable(sessionrelay.ReasonEnded), "workbench_turn_undeliverable",
			"会话当前无法接收消息：会话已结束（" + undeliverable(sessionrelay.ReasonEnded).Error() + "）"},
		{"inject failed", undeliverable(sessionrelay.InjectFailedPrefix + "pane_missing"), "workbench_turn_undeliverable",
			"会话当前无法接收消息：无法把消息输入终端（pane_missing）（" + undeliverable(sessionrelay.InjectFailedPrefix+"pane_missing").Error() + "）"},
		{"no open turn", sessionrelay.ErrNoOpenTurn, "workbench_turn_no_open_turn", "会话当前无法接收消息：它没有在等待回复"},
		{"session ended", sessionrelay.ErrSessionEnded, "workbench_turn_ended", "会话当前无法接收消息：会话已结束"},
		{"relay delivery without reason", workbench.ErrRelayDelivery, "workbench_turn_undeliverable_plain",
			"会话当前无法接收消息：" + workbench.ErrRelayDelivery.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, args, ok := workbenchTurnError(i18n.ZhCN, tc.err)
			if !ok || code != tc.code {
				t.Fatalf("code = %q ok=%v, want %q", code, ok, tc.code)
			}
			if got := newAPIError(i18n.ZhCN, 409, "m", code, args...).Detail; got != tc.zh {
				t.Errorf("zh detail = %q, want %q", got, tc.zh)
			}
			enCode, enArgs, _ := workbenchTurnError(i18n.EN, tc.err)
			en := newAPIError(i18n.EN, 409, "m", enCode, enArgs...)
			if en.Code != tc.code || errKeyCJK.MatchString(en.Detail) {
				t.Errorf("en = %+v, want the same code and English text", en)
			}
		})
	}
	if _, _, ok := workbenchTurnError(i18n.ZhCN, errors.New("boom")); ok {
		t.Error("an unrelated error must fall through to its own text")
	}
}

// TestUndeliverableReasonTextIsComplete: every reason has a Chinese text equal to the
// pre-migration one and an English text without CJK; an unknown reason is shown as it is.
func TestUndeliverableReasonTextIsComplete(t *testing.T) {
	t.Parallel()
	zh := map[string]string{
		sessionrelay.ReasonEnded:                                       "会话已结束",
		sessionrelay.ReasonNoRunner:                                    "会话没有登记执行机",
		sessionrelay.DeliverFailedPrefix + sessionrelay.ReasonNoRunner: "会话没有登记执行机",
		sessionrelay.ReasonNoProject:                                   "会话所在目录不属于任何已配置项目，无法派发传话人",
		sessionrelay.ReasonNoMessenger:                                 "传话人执行器未配置",
		sessionrelay.ReasonNoTmux:                                      "会话不在 tmux 里，也没有其他可用的投递方式",
		sessionrelay.HandedOffPrefix + "job-1":                         "会话已被接管，请先释放接管",
		sessionrelay.InjectFailedPrefix + "pane_busy:vim":              "无法把消息输入终端（pane_busy:vim）",
		sessionrelay.DeliverFailedPrefix + "exit 1":                    "投递命令失败",
		"": "未知原因",
	}
	for reason, want := range zh {
		if got := undeliverableReasonText(i18n.ZhCN, reason); got != want {
			t.Errorf("zh %q = %q, want %q", reason, got, want)
		}
		if got := undeliverableReasonText(i18n.EN, reason); got == "" || errKeyCJK.MatchString(got) {
			t.Errorf("en %q = %q, want English", reason, got)
		}
	}
	if got := undeliverableReasonText(i18n.EN, "something_else"); got != "something_else" {
		t.Errorf("unknown reason = %q, want it as it is", got)
	}
}

// TestAuthPresetsFollowLanguage: the preset labels are shown text.
func TestAuthPresetsFollowLanguage(t *testing.T) {
	t.Parallel()
	var zh, en []string
	for _, p := range authPresetsIn(i18n.ZhCN) {
		zh = append(zh, p.Label)
	}
	for _, p := range authPresetsIn(i18n.EN) {
		en = append(en, p.Label)
	}
	if got := strings.Join(zh, "|"); got != "隧道使用|发版升级|代答（人用）|只读他人" {
		t.Errorf("zh labels = %s", got)
	}
	if got := strings.Join(en, "|"); errKeyCJK.MatchString(got) || len(en) != 4 {
		t.Errorf("en labels = %s", got)
	}
}

// TestCallerBoxPersonaFollowsLanguage: the capture-box name is shown text.
func TestCallerBoxPersonaFollowsLanguage(t *testing.T) {
	t.Parallel()
	if got := callerBoxPersona(i18n.EN, "admin", "default").Name; got != "admin's capture box" {
		t.Errorf("en named box = %q", got)
	}
	if got := callerBoxPersona(i18n.EN, "", "").Name; got != "Capture box" {
		t.Errorf("en anonymous box = %q", got)
	}
}

// TestStewardCheckAgentFollowsServerLanguage: the steward-agent reasons have no request
// behind them; they follow the host's language source.
func TestStewardCheckAgentFollowsServerLanguage(t *testing.T) {
	t.Parallel()
	zh := stewardHost{lang: func() i18n.Lang { return i18n.ZhCN }}.CheckAgent("x")
	en := stewardHost{lang: func() i18n.Lang { return i18n.EN }}.CheckAgent("x")
	if zh == nil || zh.Error() != "没有 agent 注册表" {
		t.Errorf("zh = %v", zh)
	}
	if en == nil || errKeyCJK.MatchString(en.Error()) {
		t.Errorf("en = %v", en)
	}
}

// webErrDictFile is the console's copy of the err.* sentences.
const webErrDictFile = "../../web/src/i18n/pages/errors.ts"

// TestWebErrorDictMatchesCatalog: web/src/i18n/pages/errors.ts carries the err.<code>
// sentences the console renders from a keyed error's code + params; they must be the
// same sentences the server renders (both languages), so the language of a message never
// depends on which side produced it.
func TestWebErrorDictMatchesCatalog(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(webErrDictFile)
	if err != nil {
		t.Skipf("web sources not present: %v", err)
	}
	src := string(raw)
	entry := regexp.MustCompile(`(?m)^\s*'err\.([a-z0-9_]+)':\s*'((?:[^'\\]|\\.)*)',?\s*$`)
	section := func(name string) string {
		i := strings.Index(src, "\n  "+name+": {")
		if i < 0 {
			t.Fatalf("%s: no %q section", webErrDictFile, name)
		}
		rest := src[i+1:]
		if j := strings.Index(rest[1:], "\n  },"); j >= 0 {
			rest = rest[:j+1]
		}
		return rest
	}
	for lang, name := range map[i18n.Lang]string{i18n.ZhCN: "zh", i18n.EN: "en"} {
		entries := entry.FindAllStringSubmatch(section(name), -1)
		if len(entries) < 10 {
			t.Fatalf("%s %s: found %d entries, parser broken?", webErrDictFile, name, len(entries))
		}
		for _, m := range entries {
			want := i18n.T(lang, "err."+m[1])
			got := strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(m[2])
			if got != want {
				t.Errorf("%s err.%s differs from the server catalog\n web:    %s\n server: %s", name, m[1], got, want)
			}
			if !i18n.HasKey(lang, "err."+m[1]) {
				t.Errorf("%s err.%s is not in the server catalog", name, m[1])
			}
		}
	}
}
