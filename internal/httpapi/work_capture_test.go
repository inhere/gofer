package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestWorkItemsPatchCapturedRules(t *testing.T) {
	s := newTestServer(t, testToken, false)
	st := s.work.Store()
	c, err := st.CaptureWorkItem(jobstore.CaptureInput{Text: "随手记", Kind: jobstore.WorkKindIdea, By: "human:t"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/work-items/" + c.ID
	tok := seedJobToken(t, s, "job-cap1", "member", "")

	// A job credential cannot change a captured item at all (403), whatever the field.
	workCall(t, s, http.MethodPatch, path, tok, map[string]any{"goal": "hijack"}, 403)
	workCall(t, s, http.MethodPatch, path, tok, map[string]any{"status": "parked"}, 403)

	// A person: ordinary edits keep it captured; asking for captured on a normal item
	// is invalid.
	r := workCall(t, s, http.MethodPatch, path, testToken, map[string]any{"goal": "补一句"}, 200)
	if r.Status != jobstore.WorkCaptured || r.Goal != "补一句" {
		t.Fatalf("edit = %+v", r)
	}
	n := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "普通"}, 200)
	workCall(t, s, http.MethodPatch, "/v1/work-items/"+n.ID, testToken, map[string]any{"status": "captured"}, 400)

	// A person moving it out of captured is archiving: it works and leaves a journal line.
	r = workCall(t, s, http.MethodPatch, path, testToken, map[string]any{"status": "parked"}, 200)
	if r.Status != jobstore.WorkParked {
		t.Fatalf("archive status = %q", r.Status)
	}
	found := false
	for _, j := range r.Journal {
		found = found || strings.Contains(j.Text, "从收集箱归档")
	}
	if !found {
		t.Fatalf("no archive journal line: %+v", r.Journal)
	}
	// Once out, a job credential is back to the ordinary default deny for PATCH.
	workCall(t, s, http.MethodPatch, path, tok, map[string]any{"goal": "x"}, 403)
}

func TestWorkItemsReportCannotMoveCaptured(t *testing.T) {
	s := newTestServer(t, testToken, false)
	c, err := s.work.Store().CaptureWorkItem(jobstore.CaptureInput{Text: "随手记", Kind: jobstore.WorkKindNote, By: "human:t"})
	if err != nil {
		t.Fatal(err)
	}
	r := workCall(t, s, http.MethodPost, "/v1/work-items/"+c.ID+"/report", testToken, map[string]any{"status": "parked", "summary": "s"}, 200)
	if r.Status != jobstore.WorkCaptured {
		t.Fatalf("report moved a captured item: %q", r.Status)
	}
	workCall(t, s, http.MethodPost, "/v1/work-items/"+c.ID+"/report", testToken, map[string]any{"status": "captured"}, 400)
}
