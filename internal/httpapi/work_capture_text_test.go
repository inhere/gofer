package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
	"github.com/inhere/gofer/internal/work"
)

func TestWorkCaptureLongBodyAPI(t *testing.T) {
	s := newTestServer(t, testToken, false)
	text := "我的想法\n\n" + strings.Repeat("中文正文", 400)
	d := workCall(t, s, http.MethodPost, "/v1/work-items/capture", testToken,
		map[string]any{"text": text, "kind": "idea"}, http.StatusOK)
	assert.Eq(t, "我的想法", d.Title)
	assert.Eq(t, text, d.CaptureText)
	got := workCall(t, s, http.MethodGet, "/v1/work-items/"+d.ID, testToken, nil, http.StatusOK)
	assert.Eq(t, text, got.CaptureText)
	resp := do(t, s, http.MethodGet, "/v1/work-items?status=captured", testToken, nil)
	var list struct {
		Items []work.ItemView `json:"items"`
	}
	decode(t, resp, &list)
	assert.Require(t, assert.Len(t, list.Items, 1))
	assert.Eq(t, text, list.Items[0].CaptureText)

	workCall(t, s, http.MethodPost, "/v1/work-items/capture", testToken,
		map[string]any{"text": strings.Repeat("想", 2001)}, http.StatusBadRequest)
}
