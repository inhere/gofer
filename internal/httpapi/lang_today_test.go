package httpapi

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

var hanRe = regexp.MustCompile(`\p{Han}`)

// getWithLang GETs path with an Accept-Language header ("" = none) and decodes the body.
func getWithLang(t *testing.T, s *Server, path, acceptLanguage string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if acceptLanguage != "" {
		req.Header.Set("Accept-Language", acceptLanguage)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d: %s", path, rec.Code, rec.Body.String())
	}
	decode(t, rec.Result(), out)
}

// TestTodayTextFollowsAcceptLanguage: the today page's generated text (tags, titles, action
// labels, the digest title) follows the request language, and a request with none gets
// server.language (S3b).
func TestTodayTextFollowsAcceptLanguage(t *testing.T) {
	t.Parallel()
	for _, serverLang := range []string{"zh-CN", "en"} {
		t.Run("server="+serverLang, func(t *testing.T) {
			t.Parallel()
			s := newTestServer(t, testToken, false)
			s.projects.Config().Server.Language = serverLang
			if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{ID: "j-lang", ProjectKey: "p", Agent: "codex", Status: job.StatusNeedsReview,
				RequestJSON: `{"title":"t"}`, WorktreePath: "/wt", StartedAt: 1, UpdatedAt: 1, EndedAt: 1}); err != nil {
				t.Fatal(err)
			}
			type card struct {
				Key    string `json:"key"`
				Tag    string `json:"tag"`
				Blocks struct {
					Text string `json:"text"`
				} `json:"blocks"`
				Actions []struct {
					Label string `json:"label"`
				} `json:"actions"`
			}
			var resp struct {
				Digest struct {
					Title string `json:"title"`
				} `json:"digest"`
				Decisions []card `json:"decisions"`
			}
			read := func(h string) (card, string) {
				resp.Decisions = nil
				getWithLang(t, s, "/v1/today", h, &resp)
				for _, c := range resp.Decisions {
					if c.Key == "review:j-lang" {
						return c, resp.Digest.Title
					}
				}
				t.Fatalf("no review card for %q: %+v", h, resp.Decisions)
				return card{}, ""
			}
			zh, zhTitle := read("zh-CN")
			en, enTitle := read("en")
			if zh.Tag != "待验收" || zh.Actions[0].Label != "通过" || zh.Blocks.Text != "占着 worktree" || !strings.Contains(zhTitle, "工作摘要") {
				t.Errorf("zh-CN card = %+v title %q", zh, zhTitle)
			}
			if en.Tag != "Awaiting review" || en.Actions[0].Label != "Accept" || en.Blocks.Text != "holding a worktree" || !strings.Contains(enTitle, "Work digest") {
				t.Errorf("en card = %+v title %q", en, enTitle)
			}
			if hanRe.MatchString(en.Tag + en.Actions[0].Label + en.Blocks.Text + enTitle) {
				t.Errorf("en text carries Chinese: %+v %q", en, enTitle)
			}
			want, wantTitle := zh, zhTitle
			if serverLang == "en" {
				want, wantTitle = en, enTitle
			}
			if got, title := read(""); got.Tag != want.Tag || title != wantTitle {
				t.Errorf("no Accept-Language: tag %q title %q, want the server.language (%s) text %q / %q", got.Tag, title, serverLang, want.Tag, wantTitle)
			}

			// the work digest preview and the lanes follow the header too
			var dg struct {
				Title string `json:"title"`
				Text  string `json:"text"`
			}
			getWithLang(t, s, "/v1/work-items/digest", "en", &dg)
			if !strings.Contains(dg.Title, "Work digest") || hanRe.MatchString(dg.Text) {
				t.Errorf("en work digest = %+v", dg)
			}
			getWithLang(t, s, "/v1/work-items/digest", "zh-CN", &dg)
			if !strings.Contains(dg.Title, "工作摘要") {
				t.Errorf("zh-CN work digest = %+v", dg)
			}
		})
	}
}
