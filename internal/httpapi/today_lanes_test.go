package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/today"
)

func TestTodayLanesAndJournalLevel(t *testing.T) {
	s := newTestServer(t, testToken, false)
	registerSession(t, s, "sess-lane-1", "/ws/a")
	humanPrompt(t, s, "sess-lane-1", "ws: 导出接口")
	items, _ := workList(t, s, "")
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	id := items[0].ID

	// A person's note is a milestone; ?level=milestone filters; a bad level is a 400.
	note := func(body map[string]any, want int) {
		t.Helper()
		resp := do(t, s, http.MethodPost, "/v1/work-items/"+id+"/journal", testToken, body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("note %v = %d, want %d", body, resp.StatusCode, want)
		}
	}
	note(map[string]any{"text": "分页方案定了"}, 200)
	note(map[string]any{"text": "顺手记一笔", "level": "detail"}, 200)
	note(map[string]any{"text": "x", "level": "loud"}, 400)
	resp := do(t, s, http.MethodGet, "/v1/work-items/"+id+"/journal?level=milestone", testToken, nil)
	var j struct {
		Journal []struct {
			Text  string `json:"text"`
			Level string `json:"level"`
		} `json:"journal"`
	}
	decode(t, resp, &j)
	for _, e := range j.Journal {
		if e.Level != "milestone" || e.Text == "顺手记一笔" {
			t.Fatalf("milestone filter leaked %+v", e)
		}
	}
	if len(j.Journal) == 0 || j.Journal[len(j.Journal)-1].Text != "分页方案定了" {
		t.Fatalf("milestones = %+v", j.Journal)
	}
	if resp := do(t, s, http.MethodGet, "/v1/work-items/"+id+"/journal?level=loud", testToken, nil); resp.StatusCode != 400 {
		t.Fatalf("bad level = %d", resp.StatusCode)
	}

	// The item view carries health + milestones.
	resp = do(t, s, http.MethodGet, "/v1/work-items/"+id, testToken, nil)
	var d struct {
		Health     string `json:"health"`
		Milestones []struct {
			Text string `json:"text"`
		} `json:"milestones"`
	}
	decode(t, resp, &d)
	if d.Health != "ok" || len(d.Milestones) == 0 || d.Milestones[len(d.Milestones)-1].Text != "分页方案定了" {
		t.Fatalf("view = %+v", d)
	}

	resp = do(t, s, http.MethodGet, "/v1/today/lanes", testToken, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("lanes = %d", resp.StatusCode)
	}
	var v today.LanesView
	decode(t, resp, &v)
	if v.Summary.Total != 1 || len(v.Lanes) != 1 {
		t.Fatalf("lanes = %+v", v)
	}
	l := v.Lanes[0]
	if l.Kind != today.LaneWork || l.ID != id || l.Progress.Current != "分页方案定了" || l.Health != "ok" || l.Links.WorkItemID != id {
		t.Fatalf("lane = %+v", l)
	}
}

func TestTodayLanesWorkerTokenRefused(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/today/lanes", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", resp.StatusCode)
	}
}
