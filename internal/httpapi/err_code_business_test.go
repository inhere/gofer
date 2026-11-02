package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// postErrWithLang is POST path (JSON body) with an Accept-Language header, decoded as an
// error body.
func postErrWithLang(t *testing.T, s *Server, path string, body any, acceptLanguage string) (int, errorBody) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", acceptLanguage)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out errorBody
	decode(t, rec.Result(), &out)
	return rec.Code, out
}

// assertKeyedBody: the body is the keyed form (code + params), the zh-CN detail is the
// Chinese sentence verbatim, the en detail has no CJK.
func assertKeyedBody(t *testing.T, status int, zh, en errorBody, wantStatus int, code, zhDetail string) {
	t.Helper()
	if status != wantStatus || zh.Code != code || zh.Detail != zhDetail {
		t.Errorf("zh-CN = %d code=%q detail=%q, want %d %q %q", status, zh.Code, zh.Detail, wantStatus, code, zhDetail)
	}
	if en.Code != code || en.Detail == "" || errKeyCJK.MatchString(en.Detail) || errKeyCJK.MatchString(en.Error) {
		t.Errorf("en = code=%q error=%q detail=%q, want English text for %q", en.Code, en.Error, en.Detail, code)
	}
	if zh.Error != en.Error {
		t.Errorf("the error phrase must not follow the language: zh=%q en=%q", zh.Error, en.Error)
	}
}

// TestWorkbenchBusinessErrorsAreKeyed: the workbench service's Chinese-era errors reach
// the HTTP detail through a code, in the request language; the Chinese detail is the
// sentence the service used to return and the `error` phrase stays fixed.
func TestWorkbenchBusinessErrorsAreKeyed(t *testing.T) {
	t.Parallel()
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "nobase", SessionID: "nobase-s", Status: job.StatusDone, StartedAt: now - 10, UpdatedAt: now - 5}, "t", "p")

	zhS, zh := getErrWithLang(t, s, "/v1/workbench/threads/s:nobase-s/diff", "zh-CN")
	enS, en := getErrWithLang(t, s, "/v1/workbench/threads/s:nobase-s/diff", "en")
	assertKeyedBody(t, zhS, zh, en, http.StatusConflict, "diff_base_missing", "首轮作业没有 base_sha/worktree_base_sha，无法建立会话改动基线")
	_ = enS
	if zh.Error != "get workbench thread diff failed" {
		t.Errorf("error phrase = %q", zh.Error)
	}

	zhS, zh = getErrWithLang(t, s, "/v1/workbench/threads/r:some-relay/diff", "zh-CN")
	_, en = getErrWithLang(t, s, "/v1/workbench/threads/r:some-relay/diff", "en")
	assertKeyedBody(t, zhS, zh, en, http.StatusConflict, "diff_relay_no_job", "relay 会话没有可审阅的 job 改动")

	body := map[string]any{"summary": "x"}
	zhS, zh = postErrWithLang(t, s, "/v1/workbench/threads/j:nobase/review", body, "zh-CN")
	_, en = postErrWithLang(t, s, "/v1/workbench/threads/j:nobase/review", body, "en")
	assertKeyedBody(t, zhS, zh, en, http.StatusConflict, "thread_no_agent_session", "该会话没有可续接的 agent session")
}

// TestWorkerUpgradeTooOldIsKeyed: the hub's coded refusal survives the serve-layer
// wrapping and the handler answers it as a keyed 409 with the placeholder params.
func TestWorkerUpgradeTooOldIsKeyed(t *testing.T) {
	t.Parallel()
	s, fu, mgr := newUpgradeServer(t, map[string]WorkerStatus{"w1": connectedWorker(runtime.GOOS, runtime.GOARCH, 15)})
	coded := i18n.Coded(errors.New("sentinel"), "worker_upgrade_too_old", "worker w1 speaks protocol v3", "worker", "w1", "have", 3, "need", 15)
	fu.err = fmt.Errorf("%w: %w", ErrUpgradeTooOld, coded)

	zhS, zh := postErrWithLang(t, s, "/v1/workers/w1/upgrade", map[string]any{}, "zh-CN")
	_, en := postErrWithLang(t, s, "/v1/workers/w1/upgrade", map[string]any{}, "en")
	assertKeyedBody(t, zhS, zh, en, http.StatusConflict, "worker_upgrade_too_old",
		"worker w1 的协议版本是 v3，远程升级需要 v15 及以上（该 worker 版本过旧，需要手动升级一次）")
	if zh.Params["worker"] != "w1" || zh.Params["have"] != float64(3) || zh.Params["need"] != float64(15) {
		t.Errorf("params = %v", zh.Params)
	}
	// The stored upgrade record keeps the English log text (it is not a response).
	if rec, ok := mgr.Latest("w1"); !ok || errKeyCJK.MatchString(rec.Error) {
		t.Errorf("record = %+v", rec)
	}
}
