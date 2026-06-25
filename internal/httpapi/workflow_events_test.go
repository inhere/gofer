package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job/workflow"
)

// TestListWorkflowEventsAPI asserts GET /v1/workflows/{id}/events returns the
// workflow's append-only event timeline (submitted ... terminal) for a completed
// workflow, with seq-ordered events and a working ?since cursor.
func TestListWorkflowEventsAPI(t *testing.T) {
	s := newTestServer(t, testToken, false)

	resp := do(t, s, http.MethodPost, "/v1/workflows", testToken, workflow.Spec{
		Steps: []workflow.StepSpec{echoStep("one"), echoStep("two")},
	})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, resp, &created)
	waitWorkflowStatus(t, s, created.ID, "done")

	resp = do(t, s, http.MethodGet, "/v1/workflows/"+created.ID+"/events", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Events []struct {
			Seq  int64  `json:"seq"`
			Type string `json:"type"`
		} `json:"events"`
	}
	decode(t, resp, &body)
	if len(body.Events) == 0 {
		t.Fatal("no workflow events returned")
	}
	// seq strictly increasing.
	for i := 1; i < len(body.Events); i++ {
		if body.Events[i].Seq <= body.Events[i-1].Seq {
			t.Fatalf("events not seq-ordered at %d", i)
		}
	}
	if body.Events[0].Type != "workflow.submitted" {
		t.Fatalf("first event = %s, want workflow.submitted", body.Events[0].Type)
	}
	if last := body.Events[len(body.Events)-1]; last.Type != "workflow.terminal" {
		t.Fatalf("last event = %s, want workflow.terminal", last.Type)
	}

	// ?since cursor returns only events after the first seq.
	firstSeq := body.Events[0].Seq
	resp = do(t, s, http.MethodGet, "/v1/workflows/"+created.ID+"/events?since="+itoa(firstSeq), testToken, nil)
	var tail struct {
		Events []struct {
			Seq int64 `json:"seq"`
		} `json:"events"`
	}
	decode(t, resp, &tail)
	for _, e := range tail.Events {
		if e.Seq <= firstSeq {
			t.Fatalf("since=%d returned event seq %d (<= cursor)", firstSeq, e.Seq)
		}
	}
	if len(tail.Events) != len(body.Events)-1 {
		t.Fatalf("since returned %d events, want %d", len(tail.Events), len(body.Events)-1)
	}
}

// TestListWorkflowEventsUnknown404 asserts an unknown workflow id is a 404.
func TestListWorkflowEventsUnknown404(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/workflows/nope/events", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

// itoa is a tiny int64->string helper for query building (avoids importing strconv
// in the test for one call).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
