package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/i18n"
)

// planWithLang fetches a session's takeover plan, optionally with an
// Accept-Language header.
func planWithLang(t *testing.T, s *Server, sid, acceptLanguage string) resumePlanBody {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sid+"/takeover-plan", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("takeover-plan status = %d", rec.Code)
	}
	var plan resumePlanBody
	decode(t, rec.Result(), &plan)
	return plan
}

// TestTakeoverPlanFollowsAcceptLanguage is the S3a demonstration wiring: the
// takeover plan's human text follows the request's Accept-Language (q order, first
// understood language), and a request with none (or none understood) gets
// server.language.
func TestTakeoverPlanFollowsAcceptLanguage(t *testing.T) {
	t.Parallel()
	for _, serverLang := range []string{"zh-CN", "en"} {
		t.Run("server="+serverLang, func(t *testing.T) {
			t.Parallel()
			s := newTestServer(t, testToken, false)
			s.projects.Config().Server.Language = serverLang
			s.relay.SetTakeoverer(&httpTakeoverer{})
			registerTakeoverSession(t, s, "sid-lang", "/w/repo/sub", "")
			endSession(t, s, "sid-lang")

			zh := planWithLang(t, s, "sid-lang", "zh-CN")
			en := planWithLang(t, s, "sid-lang", "en")
			if !strings.Contains(zh.Message, "会话已结束") {
				t.Errorf("Accept-Language zh-CN message = %q, want Chinese", zh.Message)
			}
			// The "Basis: ..." tail (the cwd-choice reason) is in the catalog too (S3b).
			if !strings.Contains(en.Message, "The session has ended") || strings.Contains(en.Message, "会话已结束") {
				t.Errorf("Accept-Language en message = %q, want English", en.Message)
			}
			if !strings.Contains(zh.Message, "依据：") || !strings.Contains(zh.Message, "改用登记的目录") {
				t.Errorf("zh-CN message lacks the Chinese basis sentence: %q", zh.Message)
			}
			if strings.Contains(en.Message, "依据") || regexp.MustCompile(`\p{Han}`).MatchString(en.Message) || !strings.Contains(en.Message, "Basis:") {
				t.Errorf("en message carries Chinese or lacks the basis: %q", en.Message)
			}
			// q order: the higher-q language wins; an unknown tag is skipped.
			if got := planWithLang(t, s, "sid-lang", "fr;q=0.9, zh-CN;q=0.5, en;q=0.4"); got.Message != zh.Message {
				t.Errorf("q-ordered header message = %q, want the zh-CN text", got.Message)
			}
			if got := planWithLang(t, s, "sid-lang", "fr, en;q=0.4"); got.Message != en.Message {
				t.Errorf("header with an unknown first tag message = %q, want the en text", got.Message)
			}
			// No header, or none understood: server.language.
			want := zh.Message
			if serverLang == "en" {
				want = en.Message
			}
			for _, h := range []string{"", "fr", "*"} {
				if got := planWithLang(t, s, "sid-lang", h); got.Message != want {
					t.Errorf("Accept-Language %q message = %q, want the server.language (%s) text", h, got.Message, serverLang)
				}
			}
		})
	}
}

// TestRequestLangReachesContext pins the carrier business code reads: the
// middleware stores the canonical language in the request context.
func TestRequestLangReachesContext(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	s.projects.Config().Server.Language = "en"
	for header, want := range map[string]i18n.Lang{"": i18n.EN, "zh": i18n.ZhCN, "en-US,zh;q=0.5": i18n.EN, "zh-TW": i18n.ZhCN, "de": i18n.EN} {
		var got i18n.Lang
		r := rux.New()
		r.Use(s.requestLangMiddleware)
		r.GET("/__lang_probe", func(c *rux.Context) { got = i18n.FromContext(c.Req.Context()) })
		req := httptest.NewRequest(http.MethodGet, "/__lang_probe", nil)
		if header != "" {
			req.Header.Set("Accept-Language", header)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
		if got != want {
			t.Errorf("Accept-Language %q -> context language %q, want %q", header, got, want)
		}
	}
}

// sessionsWithLang GETs path with an Accept-Language header and decodes the body.
func sessionsWithLang(t *testing.T, s *Server, path, acceptLanguage string, into any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d", path, rec.Code)
	}
	decode(t, rec.Result(), into)
}

// TestSessionResumeMessageFollowsAcceptLanguage: the resume_message of every row of
// GET /v1/sessions and of GET /v1/sessions/{sid} follows the request language (S3b),
// not the session project's.
func TestSessionResumeMessageFollowsAcceptLanguage(t *testing.T) {
	t.Parallel()
	for _, serverLang := range []string{"zh-CN", "en"} {
		t.Run("server="+serverLang, func(t *testing.T) {
			t.Parallel()
			s := newTestServer(t, testToken, false)
			s.projects.Config().Server.Language = serverLang
			s.relay.SetTakeoverer(&httpTakeoverer{})
			registerTakeoverSession(t, s, "sid-rm", "/w/repo/sub", "")
			endSession(t, s, "sid-rm")

			message := func(acceptLanguage string) (list, detail string) {
				var l struct {
					Sessions []sessionView `json:"sessions"`
				}
				sessionsWithLang(t, s, "/v1/sessions?all=1", acceptLanguage, &l)
				for _, v := range l.Sessions {
					if v.SessionID == "sid-rm" {
						list = v.ResumeMessage
					}
				}
				var d struct {
					Session sessionView `json:"session"`
				}
				sessionsWithLang(t, s, "/v1/sessions/sid-rm", acceptLanguage, &d)
				return list, d.Session.ResumeMessage
			}
			zhList, zhDetail := message("zh-CN")
			enList, enDetail := message("en")
			if !strings.Contains(zhList, "会话已结束") || zhList != zhDetail {
				t.Errorf("zh-CN list=%q detail=%q, want the same Chinese text", zhList, zhDetail)
			}
			if !strings.Contains(enList, "The session has ended") || enList != enDetail || regexp.MustCompile(`\p{Han}`).MatchString(enList) {
				t.Errorf("en list=%q detail=%q, want the same English text", enList, enDetail)
			}
			want := zhList
			if serverLang == "en" {
				want = enList
			}
			if got, _ := message(""); got != want {
				t.Errorf("no Accept-Language list = %q, want the server.language (%s) text", got, serverLang)
			}
		})
	}
}
