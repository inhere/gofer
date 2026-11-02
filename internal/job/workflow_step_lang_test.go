package job

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
)

// TestWorkflowStepJobCarriesProjectLanguage: a built-in step runs as `gofer tool step`
// with no request language, so its job's env carries the project's language (project
// beats server); a step's own GOFER_LANG wins and other jobs are left alone.
func TestWorkflowStepJobCarriesProjectLanguage(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:   config.ServerConfig{Language: "zh-CN"},
		Projects: map[string]config.ProjectConfig{"en-proj": {Language: "en"}, "plain": {}},
	}
	cases := []struct {
		name string
		req  JobRequest
		want string
	}{
		{"project language", JobRequest{ProjectKey: "en-proj", InternalExec: InternalExecWorkflowStep}, "en"},
		{"server language", JobRequest{ProjectKey: "plain", InternalExec: InternalExecWorkflowStep}, "zh-CN"},
		{"step env wins", JobRequest{ProjectKey: "en-proj", InternalExec: InternalExecWorkflowStep, Env: map[string]string{i18n.EnvLanguageVar: "zh-CN"}}, "zh-CN"},
		{"other internal exec", JobRequest{ProjectKey: "en-proj", InternalExec: InternalExecDelivery}, ""},
		{"ordinary job", JobRequest{ProjectKey: "en-proj"}, ""},
	}
	for _, tc := range cases {
		req := tc.req
		stampWorkflowStepLang(cfg, &req)
		if got := req.Env[i18n.EnvLanguageVar]; got != tc.want {
			t.Errorf("%s: GOFER_LANG = %q, want %q", tc.name, got, tc.want)
		}
	}
}
