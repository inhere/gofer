package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSettingsClientRoutes exercises every settings call against one stub, so the path,
// the method and the body of each are pinned to the /v1/settings contract.
func TestSettingsClientRoutes(t *testing.T) {
	type call struct{ method, uri, body, contentType string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.RequestURI(), string(b), r.Header.Get("Content-Type")})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"settings": []any{
				map[string]any{"key": "work.draft.min_prompts", "kind": "int", "default": 2, "value": 2, "modified": false},
				map[string]any{"key": "work.draft.exclude_projects", "kind": "strings", "default": []string{}, "value": []string{"demo"}, "modified": true},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/settings/export":
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("work.draft.min_prompts: 3\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/settings/import":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dry_run": true, "applied": false, "errors": 0,
				"changes": []any{map[string]any{"key": "work.draft.min_prompts", "action": "set", "old": 2, "new": 3}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/settings/work.draft.min_prompts":
			_ = json.NewEncoder(w).Encode(map[string]any{"key": "work.draft.min_prompts", "kind": "int", "value": 2, "default": 2})
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "unknown setting", "detail": "no setting named nope"})
		case r.Method == http.MethodDelete:
			_ = json.NewEncoder(w).Encode(map[string]any{"key": "work.draft.min_prompts", "modified": false, "value": 2, "reset": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"key": "work.draft.min_prompts", "kind": "int", "value": 3, "modified": true})
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	items, err := c.ListSettings()
	if err != nil || len(items) != 2 || items[1].Value.([]any)[0] != "demo" {
		t.Fatalf("ListSettings: %+v %v", items, err)
	}
	item, err := c.GetSetting("work.draft.min_prompts")
	if err != nil || item.Kind != "int" || item.Value.(float64) != 2 {
		t.Fatalf("GetSetting: %+v %v", item, err)
	}
	if _, err := c.GetSetting("nope"); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Fatalf("GetSetting unknown key should surface the server error, got %v", err)
	}
	set, err := c.SetSetting("work.draft.min_prompts", 3)
	if err != nil || !set.Modified || set.Value.(float64) != 3 {
		t.Fatalf("SetSetting: %+v %v", set, err)
	}
	// The value must be sent as the JSON type the key expects, not as a string.
	if last := calls[len(calls)-1]; last.method != http.MethodPut || last.body != `{"value":3}` {
		t.Fatalf("SetSetting request: %+v", last)
	}
	reset, err := c.ResetSetting("work.draft.min_prompts")
	if err != nil || !reset.Reset || reset.Key != "work.draft.min_prompts" {
		t.Fatalf("ResetSetting: %+v %v", reset, err)
	}
	// A list value keeps its shape.
	if _, err := c.SetSetting("work.draft.exclude_projects", []string{"demo", "sandbox"}); err != nil {
		t.Fatalf("SetSetting list: %v", err)
	}
	if last := calls[len(calls)-1]; last.body != `{"value":["demo","sandbox"]}` {
		t.Fatalf("list value body: %s", last.body)
	}

	doc, err := c.ExportSettings()
	if err != nil || string(doc) != "work.draft.min_prompts: 3\n" {
		t.Fatalf("ExportSettings: %q %v", doc, err)
	}
	imp, err := c.ImportSettings([]byte("work.draft.min_prompts: 3\n"), true)
	if err != nil || !imp.DryRun || len(imp.Changes) != 1 || imp.Changes[0].Action != "set" {
		t.Fatalf("ImportSettings: %+v %v", imp, err)
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last.uri, "dry_run=1") || last.contentType != "application/yaml" {
		t.Fatalf("import request: %+v", last)
	}
	if _, err := c.ImportSettings([]byte("x: 1\n"), false); err != nil {
		t.Fatalf("ImportSettings write: %v", err)
	}
	if last := calls[len(calls)-1]; strings.Contains(last.uri, "dry_run") {
		t.Fatalf("a write import must not carry dry_run: %s", last.uri)
	}
}

// TestDoRawReturnsNon2xxAsData: a raw call reports a failing status as data (the caller
// prints it), never as a transport error.
func TestDoRawReturnsNon2xxAsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("raw call lost the token: %q", got)
		}
		w.Header().Set("X-Test", "1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	resp, err := c.DoRaw(http.MethodGet, "/v1/settings", nil, "")
	if err != nil {
		t.Fatalf("DoRaw: %v", err)
	}
	if resp.Status != http.StatusForbidden || !strings.Contains(string(resp.Body), "forbidden") {
		t.Fatalf("DoRaw answer: %+v", resp)
	}
	if resp.Header.Get("X-Test") != "1" {
		t.Fatalf("headers not returned: %+v", resp.Header)
	}
	// errorFor still turns the same body into the friendly error the rest of the CLI uses.
	if err := errorFor(resp.Status, resp.Body); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("errorFor: %v", err)
	}
}
