package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSessionListCarriesSeatsOfTheWholePage: the session list answers the `seat` object of every
// session from ONE prefetched lookup — a session that sits in a seat shows it, one that sits in
// none does not.
func TestSessionListCarriesSeatsOfTheWholePage(t *testing.T) {
	s := newCeilingServer(t)
	x := seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats", map[string]any{
		"project_key": "p1", "name": "dev", "purpose": "pr review", "charter": "@docs/charter.md",
	}, 200)
	registerSeatSession(t, s, "sess-in-seat", "p1", "plain")
	registerSeatSession(t, s, "sess-free", "p1", "plain")
	seatJSON(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+x.ID+"/bind", map[string]any{"ref": "sess-in-seat"}, 200)

	status, resp := seatReq(t, s, ceilTokPlain, "", "GET", "/v1/sessions", nil)
	if status != http.StatusOK {
		t.Fatalf("list sessions = %d: %s", status, resp)
	}
	var got struct {
		Sessions []struct {
			SessionID string           `json:"session_id"`
			Seat      *sessionSeatView `json:"seat"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(resp), &got); err != nil {
		t.Fatalf("decode %s: %v", resp, err)
	}
	seen := 0
	for _, a := range got.Sessions {
		switch a.SessionID {
		case "sess-in-seat":
			seen++
			if a.Seat == nil || a.Seat.ID != x.ID || a.Seat.Name != "dev" || a.Seat.Purpose != "pr review" {
				t.Fatalf("the session that sits in a seat must carry it: %+v", a.Seat)
			}
			if a.Seat.CharterPath != "docs/charter.md" || a.Seat.Root == "" {
				t.Fatalf("a path charter fills charter_path and root: %+v", a.Seat)
			}
		case "sess-free":
			seen++
			if a.Seat != nil {
				t.Fatalf("a session in no seat must not carry one: %+v", a.Seat)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("the list must carry both sessions, got %d of the two seeded ones: %s", seen, resp)
	}
}

// TestBindAnswerReportsTheCharterMessage: binding an acp seat whose job has ended says nothing (the
// restart carries the charter), so the bind answer carries neither charter_sent nor charter_error.
func TestBindAnswerReportsTheCharterMessage(t *testing.T) {
	s := newCeilingServer(t)
	x := seedEndedACPSeat(t, s, "plain")
	status, resp := seatReq(t, s, ceilTokPlain, "", "POST", "/v1/seats/"+x.ID+"/bind", map[string]any{"ref": "acp-old-plain", "replace": true})
	if status != http.StatusOK {
		t.Fatalf("bind = %d: %s", status, resp)
	}
	var v seatView
	if err := json.Unmarshal([]byte(resp), &v); err != nil {
		t.Fatalf("decode %s: %v", resp, err)
	}
	if v.CharterSent || v.CharterError != "" {
		t.Fatalf("an ended job gets no charter message: %+v", v)
	}
}
