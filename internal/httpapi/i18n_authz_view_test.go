package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
)

var authzViewHan = regexp.MustCompile(`\p{Han}`)

func authzViewGet(t *testing.T, s *Server, path, lang string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer tok-root")
	req.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body)
	}
	decode(t, rec.Result(), out)
}

// TestAuthCatalogFollowsRequestLanguage: the permission catalog the console shows is in the
// request language (the generated reference keeps zh-CN): zh-CN is the text the catalog had
// before it moved into the language files, English carries no CJK.
func TestAuthCatalogFollowsRequestLanguage(t *testing.T) {
	t.Parallel()
	s := authTestServer(t)
	var zh, en authCatalogView
	authzViewGet(t, s, "/v1/auth/catalog", "zh-CN", &zh)
	authzViewGet(t, s, "/v1/auth/catalog", "en", &en)
	if len(zh.Permissions) != len(authz.Permissions()) || len(en.Permissions) != len(zh.Permissions) {
		t.Fatalf("catalog sizes zh=%d en=%d", len(zh.Permissions), len(en.Permissions))
	}
	for i, p := range authz.Permissions() {
		z, e := zh.Permissions[i], en.Permissions[i]
		if z.Description != p.DescriptionIn("zh-CN") || z.RiskLabel != p.Risk.Label() || z.ScopeLabel != p.Scope.Label() || z.SourceLabel != p.Source.Label() {
			t.Errorf("%s zh = %+v", p.Name, z)
		}
		for _, text := range []string{e.Description, e.RiskLabel, e.ScopeLabel, e.SourceLabel} {
			if text == "" || authzViewHan.MatchString(text) {
				t.Errorf("%s en carries %q", p.Name, text)
			}
		}
	}
	var ex authExpandView
	authzViewGet(t, s, "/v1/auth/expand?pattern=tunnel.open", "en", &ex)
	if len(ex.Permissions) != 1 || authzViewHan.MatchString(ex.Permissions[0].Description+ex.Permissions[0].RiskLabel) {
		t.Errorf("en expand = %+v", ex.Permissions)
	}
	authzViewGet(t, s, "/v1/auth/expand?pattern=tunnel.open", "zh-CN", &ex)
	if ex.Permissions[0].Description != "开隧道连接" || ex.Permissions[0].RiskLabel == "" {
		t.Errorf("zh expand = %+v", ex.Permissions)
	}
}

// TestSettingsListFollowsRequestLanguage: the settings page's descriptions are in the
// request language (zh-CN is the text the registry had before it moved into the catalog).
func TestSettingsListFollowsRequestLanguage(t *testing.T) {
	s := settingsTestServer(t)
	get := func(path, lang string) []settingsItemDesc {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Accept-Language", lang)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		var out struct {
			Settings []settingsItemDesc `json:"settings"`
		}
		decode(t, rec.Result(), &out)
		return out.Settings
	}
	en, zh := get("/v1/settings", "en"), get("/v1/settings", "zh-CN")
	if len(en) == 0 || len(en) != len(zh) {
		t.Fatalf("listing sizes %d / %d", len(en), len(zh))
	}
	for i := range en {
		if en[i].Description == "" || authzViewHan.MatchString(en[i].Description) {
			t.Errorf("%s en description = %q", en[i].Key, en[i].Description)
		}
		if !authzViewHan.MatchString(zh[i].Description) {
			t.Errorf("%s zh description = %q", zh[i].Key, zh[i].Description)
		}
	}
}

type settingsItemDesc struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

// TestSummarizerCheckFollowsLanguage: the summarizer host's refusal reasons are the catalog's
// httpapi.summarizer.* in the language asked for; zh-CN is verbatim what the adapter returned
// before the text moved, English carries no CJK.
func TestSummarizerCheckFollowsLanguage(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"no-ro":    {Type: agent.TypeCLIAgent, Command: "x", Args: []string{"{{prompt}}"}},
		"acp-only": {Type: agent.TypeACPAgent, Command: "x"},
		"gone":     {Type: agent.TypeCLIAgent, Command: "gone", Args: []string{"{{prompt}}"}, ReadOnlyArgs: []string{"--ro"}},
	}}
	reg := agent.NewRegistryWith(cfg, map[string]agent.DetectResult{
		"no-ro": {Available: true}, "acp-only": {Available: true}, "gone": {Available: false},
	})
	o := workOneShot{agents: reg}
	want := map[string]string{
		"missing":  `整理器 agent "missing" 不存在（在 work.summarizer_agent 里改成已配置的 cli-agent）`,
		"acp-only": `整理器 agent "acp-only" 不是 cli-agent（一次性只读整理需要 cli-agent）`,
		"no-ro":    `整理器 agent "no-ro" 没有只读模式（给它配 agents.no-ro.read_only_args；claude / codex 已内置）`,
		"gone":     `整理器 agent "gone" 不可用：本机没有找到该命令`,
	}
	for key, zh := range want {
		if err := o.CheckIn(i18n.ZhCN, key); err == nil || err.Error() != zh {
			t.Errorf("zh CheckIn(%q) = %v, want %q", key, err, zh)
		}
		if err := o.CheckIn(i18n.EN, key); err == nil || authzViewHan.MatchString(err.Error()) {
			t.Errorf("en CheckIn(%q) = %v", key, err)
		}
	}
	if err := (workOneShot{}).CheckIn(i18n.ZhCN, "x"); err == nil || err.Error() != "没有 agent 注册表" {
		t.Errorf("no registry = %v", err)
	}
}
