package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/streaming"
)

func writeTurns(t *testing.T, resultDir, content string) {
	t.Helper()
	assert.NoErr(t, os.WriteFile(filepath.Join(resultDir, store.TurnsFile), []byte(content), 0o644))
}

// GET /v1/jobs/{id}/transcript pages a job's per-turn record; GET /v1/jobs/{id} carries
// turn_count (and the list endpoint does not).
func TestJobTranscriptEndpointAndTurnCount(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	created := createDoneExecJob(t, s)
	writeTurns(t, created.ResultDir,
		`{"turn":1,"role":"assistant","text":"first answer"}`+"\n"+
			`{"turn":2,"role":"assistant","text":"second answer"}`+"\n")

	var page streaming.JobTranscriptPage
	resp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/transcript", testToken, nil)
	assert.Eq(t, http.StatusOK, resp.StatusCode)
	decode(t, resp, &page)
	assert.Eq(t, streaming.TranscriptSourceTurns, page.Source)
	assert.Eq(t, 2, page.Total)
	assert.Eq(t, "first answer", page.Turns[0].Preview)

	resp = do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/transcript?turns=2", testToken, nil)
	decode(t, resp, &page)
	assert.Eq(t, 1, len(page.Turns))
	assert.Eq(t, "second answer", page.Turns[0].Text)

	// Detail response carries turn_count; the list does not.
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+created.ID, testToken, nil)
	var detail map[string]any
	decode(t, resp, &detail)
	assert.Eq(t, float64(2), detail["turn_count"])
	resp = do(t, s, http.MethodGet, "/v1/jobs", testToken, nil)
	var list struct {
		Jobs []map[string]any `json:"jobs"`
	}
	decode(t, resp, &list)
	for _, j := range list.Jobs {
		_, has := j["turn_count"]
		assert.False(t, has, "list entries must not carry turn_count")
	}
}

func TestJobTranscriptEndpointRejectsBadQueriesAndUnknownJob(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	created := createDoneExecJob(t, s)
	writeTurns(t, created.ResultDir, `{"turn":1,"role":"assistant","text":"x"}`+"\n")
	for _, q := range []string{"?head=1&tail=1", "?outline=1&search=x", "?turns=3-1", "?head=0", "?tail=-2",
		"?search=" + strings.Repeat("a", 201), "?cursor=zzz"} {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/transcript"+q, testToken, nil)
		resp.Body.Close()
		assert.Eq(t, http.StatusBadRequest, resp.StatusCode, q)
	}
	resp := do(t, s, http.MethodGet, "/v1/jobs/job-nope/transcript", testToken, nil)
	resp.Body.Close()
	assert.Eq(t, http.StatusNotFound, resp.StatusCode)
}
