package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCheckProviderResponseClassification pins the business-code parsing and the
// retry classification gofer-map1 introduced: a 200 with a non-zero code is a
// refusal, and the codes a retry cannot fix are marked permanent.
func TestCheckProviderResponseClassification(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		body      string
		wantErr   bool
		permanent bool
		code      int64
		contains  string
	}{
		{name: "dingtalk ok", kind: KindDingTalk, body: `{"errcode":0,"errmsg":"ok"}`},
		{name: "dingtalk keyword mismatch", kind: KindDingTalk,
			body:    `{"errcode":310000,"errmsg":"keywords not in content"}`,
			wantErr: true, permanent: true, code: 310000, contains: "keywords not in content"},
		{name: "dingtalk sign mismatch", kind: KindDingTalk,
			body:    `{"errcode":310000,"errmsg":"sign not match"}`,
			wantErr: true, permanent: true, code: 310000},
		{name: "dingtalk invalid token", kind: KindDingTalk,
			body:    `{"errcode":300001,"errmsg":"token is invalid or not exist"}`,
			wantErr: true, permanent: true, code: 300001},
		{name: "dingtalk rate limit", kind: KindDingTalk,
			body:    `{"errcode":130101,"errmsg":"send too fast"}`,
			wantErr: true, permanent: false, code: 130101, contains: "send too fast"},
		{name: "dingtalk unknown code retries", kind: KindDingTalk,
			body:    `{"errcode":999999,"errmsg":"weird"}`,
			wantErr: true, permanent: false, code: 999999},
		{name: "feishu ok", kind: KindFeishu, body: `{"code":0,"msg":"success","data":{}}`},
		{name: "feishu keyword not found", kind: KindFeishu,
			body:    `{"code":19024,"msg":"Key Words Not Found"}`,
			wantErr: true, permanent: true, code: 19024, contains: "Key Words Not Found"},
		{name: "feishu sign mismatch", kind: KindFeishu,
			body:    `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`,
			wantErr: true, permanent: true, code: 19021},
		{name: "feishu frequency limited", kind: KindFeishu,
			body:    `{"code":11232,"msg":"frequency limited"}`,
			wantErr: true, permanent: false, code: 11232, contains: "frequency limited"},
		{name: "feishu StatusCode spelling", kind: KindFeishu,
			body:    `{"StatusCode":19024,"StatusMessage":"Key Words Not Found"}`,
			wantErr: true, permanent: true, code: 19024, contains: "Key Words Not Found"},
		{name: "feishu string code", kind: KindFeishu,
			body:    `{"code":"19024","msg":"Key Words Not Found"}`,
			wantErr: true, permanent: true, code: 19024},
		{name: "no code field is not a refusal", kind: KindDingTalk, body: `{"ok":true}`},
		{name: "not json is not a refusal", kind: KindFeishu, body: `<html>proxy</html>`},
		{name: "generic ignores the envelope", kind: KindGeneric, body: `{"errcode":310000,"errmsg":"boom"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckProviderResponse(tc.kind, []byte(tc.body))
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want a provider error, got nil")
			}
			var perr *ProviderError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a *ProviderError", err)
			}
			if perr.Permanent != tc.permanent {
				t.Errorf("permanent = %v, want %v", perr.Permanent, tc.permanent)
			}
			if perr.Code != tc.code {
				t.Errorf("code = %d, want %d", perr.Code, tc.code)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error %q does not carry the provider text %q", err, tc.contains)
			}
			// The classification is part of the recorded text, so the console shows
			// whether a retry will ever happen.
			wantKind := "retryable"
			if tc.permanent {
				wantKind = "permanent"
			}
			if !strings.Contains(err.Error(), wantKind) {
				t.Errorf("error %q does not say %s", err, wantKind)
			}
		})
	}
}

// TestPostRequestParsesProviderResponse proves the POST side (what the delivery
// sweeper actually calls) turns a 200 + business error into an error, so a
// refused message is never recorded as delivered, while the generic kind keeps
// its status-only contract.
func TestPostRequestParsesProviderResponse(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		respBody string
		wantErr  bool
	}{
		{name: "dingtalk delivered", kind: KindDingTalk, respBody: `{"errcode":0,"errmsg":"ok"}`},
		{name: "dingtalk refused", kind: KindDingTalk, respBody: `{"errcode":310000,"errmsg":"keywords not in content"}`, wantErr: true},
		{name: "feishu delivered", kind: KindFeishu, respBody: `{"code":0,"msg":"success"}`},
		{name: "feishu refused", kind: KindFeishu, respBody: `{"code":19024,"msg":"Key Words Not Found"}`, wantErr: true},
		{name: "generic ignores body", kind: KindGeneric, respBody: `{"errcode":310000,"errmsg":"keywords not in content"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.respBody))
			}))
			defer srv.Close()

			body, err := json.Marshal(map[string]any{"msgtype": "markdown"})
			if err != nil {
				t.Fatal(err)
			}
			err = postRequest(context.Background(), tc.kind, srv.URL, "job.terminal", body, "")
			if tc.wantErr {
				if err == nil {
					t.Fatal("a 200 + non-zero business code must be an error")
				}
				if !strings.Contains(err.Error(), "rejected message") {
					t.Errorf("error %q should name the provider refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want delivered, got %v", err)
			}
		})
	}
}

// TestRenderKeywordInjection pins the 自定义关键词 support: the keyword is
// prefixed to the title (DingTalk's markdown title AND the body heading) only
// when neither the title nor the text already carries it.
func TestRenderKeywordInjection(t *testing.T) {
	m := Message{EventType: "job.terminal", Title: "job 完成", Text: "id j1 · status done", Link: "https://g/jobs/j1"}

	t.Run("dingtalk prefixes title and heading", func(t *testing.T) {
		body, err := RenderMessage(KindDingTalk, m, "gofer")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Markdown struct{ Title, Text string } `json:"markdown"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Markdown.Title != "gofer job 完成" {
			t.Errorf("markdown.title = %q, want the keyword prefix", got.Markdown.Title)
		}
		if !strings.HasPrefix(got.Markdown.Text, "### gofer job 完成") {
			t.Errorf("markdown.text heading = %q, want the keyword in the heading too", got.Markdown.Text)
		}
	})

	t.Run("feishu prefixes title", func(t *testing.T) {
		body, err := RenderMessage(KindFeishu, m, "gofer")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Content map[string]string `json:"content"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got.Content["text"], "gofer job 完成") {
			t.Errorf("text = %q, want the keyword prefix", got.Content["text"])
		}
	})

	t.Run("already present is not doubled", func(t *testing.T) {
		in := Message{EventType: "job.terminal", Title: "gofer job 完成", Text: "id j1"}
		body, err := RenderMessage(KindDingTalk, in, "gofer")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Markdown struct{ Title, Text string } `json:"markdown"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Markdown.Title != "gofer job 完成" {
			t.Errorf("title = %q, want it unchanged", got.Markdown.Title)
		}

		// The keyword inside the BODY also satisfies the provider, so no prefix.
		inBody := Message{EventType: "job.terminal", Title: "job 完成", Text: "project gofer · id j1"}
		body, err = RenderMessage(KindFeishu, inBody, "gofer")
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Content map[string]string `json:"content"`
		}
		if err := json.Unmarshal(body, &f); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(f.Content["text"], "gofer job 完成") {
			t.Errorf("text = %q, want no prefix when the body already carries the keyword", f.Content["text"])
		}
	})

	t.Run("empty keyword changes nothing", func(t *testing.T) {
		withKW, err := RenderMessage(KindFeishu, m, "")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := RenderMessage(KindFeishu, m, "   ")
		if err != nil {
			t.Fatal(err)
		}
		if string(withKW) != string(plain) {
			t.Errorf("blank keyword changed the body:\n%s\n%s", withKW, plain)
		}
	})
}
