package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestOpenSessionTurnNeedsReplyBody(t *testing.T) {
	isolateAgentSessionEnv(t)
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	_, err := c.OpenSessionTurn("s1", "q", 60, true)
	assert.NoErr(t, err)
	assert.StrContains(t, body, `"needs_reply":true`)
	_, err = c.OpenSessionTurn("s1", "q", 60, false)
	assert.NoErr(t, err)
	assert.StrNotContains(t, body, "needs_reply")
}
