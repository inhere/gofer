package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestPatchJobTitle(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created, status := createJob(t, s, testToken)
	if status != http.StatusOK {
		t.Fatalf("create status=%d, want 200", status)
	}
	resp := do(t, s, http.MethodPatch, "/v1/jobs/"+created.ID, testToken, map[string]string{"title": "补充标题"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d, want 200", resp.StatusCode)
	}
	var got job.JobResult
	decode(t, resp, &got)
	if got.Title != "补充标题" {
		t.Fatalf("title=%q, want 补充标题", got.Title)
	}
	resp = do(t, s, http.MethodPatch, "/v1/jobs/"+created.ID, testToken, map[string]string{"title": ""})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &got)
	if got.Title != "" {
		t.Fatalf("cleared title=%q, want empty", got.Title)
	}
	resp = do(t, s, http.MethodPatch, "/v1/jobs/"+created.ID, testToken, map[string]string{"title": strings.Repeat("x", job.TitleMaxRunes+1)})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("long title status=%d, want 400", resp.StatusCode)
	}
	resp = do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/events", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d, want 200", resp.StatusCode)
	}
	var events struct {
		Events []jobstore.JobEvent `json:"events"`
	}
	decode(t, resp, &events)
	found := false
	for _, ev := range events.Events {
		if ev.Type == job.EventJobTitleChanged && strings.Contains(ev.Detail, "补充标题") {
			found = true
		}
	}
	if !found {
		t.Fatalf("job.title_changed event missing: %+v", events.Events)
	}
}
