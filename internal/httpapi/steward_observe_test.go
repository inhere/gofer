package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
	"github.com/inhere/gofer/internal/steward"
)

// holdHost is a steward session host whose turn never ends, so a review stays in flight while
// the test looks at what the server does with the steward's writes.
type holdHost struct{}

func (holdHost) StartSession(steward.StartSpec) (string, error) { return "job-hold", nil }
func (holdHost) Say(string, string) error                       { return nil }
func (holdHost) End(string) error                               { return nil }
func (holdHost) CheckAgent(string) error                        { return nil }
func (holdHost) Job(id string) (steward.JobInfo, bool) {
	return steward.JobInfo{ID: id, Agent: "acpa", Live: true}, true
}

func newObserveServer(t *testing.T) (*Server, *settings.Service) {
	t.Helper()
	s := newTestServer(t, testToken, false)
	set := settings.NewService(s.jobs.Meta())
	st := steward.New(s.jobs.Meta(), s.work, holdHost{})
	st.SetConfigFn(func() (config.StewardConfig, config.WorkConfig) {
		return config.StewardConfig{Enabled: true, Agent: "acpa"}, config.WorkConfig{}
	})
	st.SetSettings(set)
	t.Cleanup(func() { _ = st.Close() })
	s.steward = st
	return s, set
}

func TestProactiveStewardReviewOnlyRecordsItsWrites(t *testing.T) {
	s, set := newObserveServer(t)
	stewTok := seedJobToken(t, s, "job-obs-steward", jobstore.JobCredentialSteward, "")
	w := workCall(t, s, http.MethodPost, "/v1/work-items", testToken, map[string]any{"title": "待巡检的事", "goal": "原目标"}, 200)
	patch := func(tok, goal string, want int) {
		t.Helper()
		resp := do(t, s, http.MethodPatch, "/v1/work-items/"+w.ID, tok, map[string]any{"goal": goal})
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("PATCH goal=%s = %d, want %d", goal, resp.StatusCode, want)
		}
	}
	goalOf := func() string { return workGet(t, s, "/v1/work-items/"+w.ID).Goal }

	// No review in flight: the steward writes as before.
	patch(stewTok, "巡检外", 200)
	if goalOf() != "巡检外" {
		t.Fatalf("goal = %q", goalOf())
	}

	// A proactive (daily) review is in flight: its writes are recorded, nothing changes.
	if _, err := s.Steward().RunReview(context.Background(), steward.ReviewOpts{Trigger: steward.TriggerDaily, Force: true}); err != nil {
		t.Fatalf("review: %v", err)
	}
	patch(stewTok, "巡检里", 202)
	resp := do(t, s, http.MethodPost, "/v1/work-items/"+w.ID+"/journal", stewTok, map[string]any{"text": "巡检里记一笔"})
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("steward journal during a proactive review = %d, want 202", resp.StatusCode)
	}
	if got := goalOf(); got != "巡检外" {
		t.Fatalf("an observed write changed the item: goal = %q", got)
	}
	d := workGet(t, s, "/v1/work-items/"+w.ID)
	for _, e := range d.Journal {
		if e.Text == "巡检里记一笔" {
			t.Fatalf("an observed journal line was written: %+v", e)
		}
	}
	// Its own record (the review summary) is not an effectful write: it still lands.
	resp = do(t, s, http.MethodPost, "/v1/steward/review-summary", stewTok, map[string]any{"text": "只记录模式下的点评"})
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("review-summary during a proactive review = %d, want 200", resp.StatusCode)
	}
	// A person's write is never held back, review or not.
	patch(testToken, "人改的", 200)

	// What was asked for is on record, for a person to read; the steward cannot read it back.
	var obs struct {
		Observations []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Body   string `json:"body"`
		} `json:"observations"`
	}
	decode(t, do(t, s, http.MethodGet, "/v1/steward/observations", testToken, nil), &obs)
	if len(obs.Observations) != 2 {
		t.Fatalf("observations = %+v", obs.Observations)
	}
	resp = do(t, s, http.MethodGet, "/v1/steward/observations", stewTok, nil)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("steward reading observations = %d, want 403", resp.StatusCode)
	}

	// steward.act_proactively lifts observe mode: the same write now applies.
	if _, _, err := set.Set(settings.KeyStewardActProactively, true, "test"); err != nil {
		t.Fatal(err)
	}
	patch(stewTok, "放开后", 200)
	if got := goalOf(); got != "放开后" {
		t.Fatalf("goal after act_proactively = %q", got)
	}
}
