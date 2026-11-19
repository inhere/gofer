package client

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeatClientRoutes(t *testing.T) {
	var calls []string
	var lastBody, lastSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastBody, lastSession = string(b), r.Header.Get(SessionHeader)
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/seats":
			_, _ = w.Write([]byte(`{"seats":[{"id":"seat-1","name":"dev","project_key":"p","state":"none","charter_by":"user","kind":"terminal"}]}`))
		case r.URL.Path == "/v1/sessions/s1/seat-candidates":
			_, _ = w.Write([]byte(`{"seats":[{"id":"seat-1","name":"dev"}]}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"deleted":"seat-1"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"seat-1","name":"dev","current_ref":"s1","resumable":true}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.sessionID = "sess-self"

	seats, err := c.ListSeats(SeatListOpts{Project: "p", All: true})
	if err != nil || len(seats) != 1 || seats[0].Name != "dev" {
		t.Fatalf("list = %+v %v", seats, err)
	}
	purpose := "pr review"
	if _, err := c.PatchSeat("seat-1", SeatPatch{Purpose: &purpose}); err != nil || lastBody != `{"purpose":"pr review"}` || lastSession != "sess-self" {
		t.Fatalf("patch body=%q session=%q err=%v", lastBody, lastSession, err)
	}
	if got, err := c.BindSeat("seat-1", "s1", true); err != nil || got.CurrentRef != "s1" || lastBody != `{"ref":"s1","replace":true}` {
		t.Fatalf("bind = %+v %q %v", got, lastBody, err)
	}
	if _, err := c.UnbindSeat("seat-1"); err != nil {
		t.Fatal(err)
	}
	if p, err := c.SeatOpenPlan("seat-1"); err != nil || !p.Resumable {
		t.Fatalf("open plan = %+v %v", p, err)
	}
	if p, err := c.RestartSeat("seat-1"); err != nil || !p.Resumable {
		t.Fatalf("restart = %+v %v", p, err)
	}
	if cands, err := c.SeatCandidates("s1"); err != nil || len(cands) != 1 {
		t.Fatalf("candidates = %+v %v", cands, err)
	}
	if _, err := c.CreateSeat(SeatCreate{ProjectKey: "p", Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteSeat("seat-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /v1/seats?all=1&project=p", "PATCH /v1/seats/seat-1", "POST /v1/seats/seat-1/bind", "POST /v1/seats/seat-1/unbind",
		"GET /v1/seats/seat-1/open-plan", "POST /v1/seats/seat-1/restart", "GET /v1/sessions/s1/seat-candidates", "POST /v1/seats", "DELETE /v1/seats/seat-1"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}
