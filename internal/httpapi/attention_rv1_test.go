package httpapi

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// seedRelayTurn registers a session and opens one relay turn askedAgo seconds ago.
func seedRelayTurn(t *testing.T, s *Server, sid string, needsReply bool, askedAgo int64) {
	t.Helper()
	st := s.jobs.Meta()
	if _, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: sid, Agent: "claude", ProjectKey: "self", Title: sid}); err != nil {
		t.Fatalf("seed session %s: %v", sid, err)
	}
	if err := st.InsertDecision(&jobstore.PlanDecision{ID: "d-" + sid, Title: "turn", Question: "q", Kind: jobstore.DecisionKindRelay,
		SessionID: sid, AskedAt: time.Now().Unix() - askedAgo, TimeoutSec: 86400, NeedsReply: needsReply}); err != nil {
		t.Fatalf("seed turn %s: %v", sid, err)
	}
}

// TestWaitingCountEqualsListRV1: of four relay turns only the marked, unsupervised, old
// enough one is 「等我」; /v1/today waiting, the inbox action column and total_unread agree,
// and the job hold is still there.
func TestWaitingCountEqualsListRV1(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedAgentJob(t, s, jobstore.JobRecord{ID: "j-held", Status: "awaiting_approval"})
	seedRelayTurn(t, s, "s-ok", true, 900)
	seedRelayTurn(t, s, "s-sup", true, 900)
	if _, err := s.jobs.Meta().AddSessionJobWatch("s-sup", "job-watched"); err != nil {
		t.Fatal(err)
	}
	seedRelayTurn(t, s, "s-young", true, 100)
	seedRelayTurn(t, s, "s-plain", false, 900)

	var td struct {
		todayWaitingResp
		RecheckAt int64 `json:"recheck_at"`
	}
	todayCall(t, s, http.MethodGet, "/v1/today", "tok-alice", nil, 200, &td)
	var in struct {
		inboxRespBody
		RecheckAt int64 `json:"recheck_at"`
	}
	todayCall(t, s, http.MethodGet, "/v1/inbox", "tok-alice", nil, 200, &in)

	want := []string{"approval:j-held", "relay:s-ok"}
	if got := keysSorted(td.Waiting); !equalStrings(got, want) {
		t.Fatalf("today waiting = %v, want %v", got, want)
	}
	var inKeys []string
	for _, it := range in.Columns["action"] {
		inKeys = append(inKeys, it.Key)
	}
	sort.Strings(inKeys)
	if !equalStrings(inKeys, want) {
		t.Fatalf("inbox action = %v, want %v", inKeys, want)
	}
	if in.TotalUnread != len(td.Waiting) {
		t.Fatalf("total_unread = %d, waiting = %d", in.TotalUnread, len(td.Waiting))
	}
	// The young turn is the only one waiting on the clock: ~200s ahead, same in both routes.
	if td.RecheckAt == 0 || td.RecheckAt != in.RecheckAt {
		t.Fatalf("recheck_at today=%d inbox=%d, want the same non-zero value", td.RecheckAt, in.RecheckAt)
	}
	if d := td.RecheckAt - time.Now().Unix(); d < 150 || d > 250 {
		t.Fatalf("recheck_at is %ds ahead, want about 200", d)
	}
}

// TestRelayWaitingAckRV1 exercises the HTTP ack path end to end: a qualifying relay
// turn is listed in today, then disappears after the user marks it read.
func TestRelayWaitingAckRV1(t *testing.T) {
	s := inboxTestServer(t, nil)
	seedRelayTurn(t, s, "s-ack", true, 900)

	var before todayWaitingResp
	todayCall(t, s, http.MethodGet, "/v1/today", "tok-alice", nil, http.StatusOK, &before)
	if got := keysSorted(before.Waiting); !equalStrings(got, []string{"relay:s-ack"}) {
		t.Fatalf("before ack waiting = %v, want relay:s-ack", got)
	}

	resp := do(t, s, http.MethodPost, "/v1/sessions/s-ack/turns/d-s-ack/ack", "tok-alice", nil)
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("ack status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	var after todayWaitingResp
	todayCall(t, s, http.MethodGet, "/v1/today", "tok-alice", nil, http.StatusOK, &after)
	if got := keysSorted(after.Waiting); len(got) != 0 {
		t.Fatalf("after ack waiting = %v, want empty", got)
	}
}
