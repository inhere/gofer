package job

import (
	"errors"
	"testing"
)

// TestSubmitSelfResult: the report is stored and shown (live and after a re-read from the
// store), never changes the job's status, a second report is ErrSelfResultExists, and bad
// input is ErrInvalidRequest.
func TestSubmitSelfResult(t *testing.T) {
	t.Parallel()
	s := newTestService(t, t.TempDir())
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{"go", "version"}, Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s, want done", final.Status)
	}
	for _, bad := range []SelfResultInput{
		{Decision: "maybe", Reason: "x"},
		{Decision: ResultPass, Reason: "  "},
	} {
		if _, err := s.SubmitSelfResult(final.ID, bad, final.ID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("SubmitSelfResult(%+v) err = %v, want ErrInvalidRequest", bad, err)
		}
	}
	got, err := s.SubmitSelfResult(final.ID, SelfResultInput{Decision: ResultFail, Reason: "could not finish", ActionRequired: "look", Findings: []string{"a"}}, final.ID)
	if err != nil {
		t.Fatalf("SubmitSelfResult: %v", err)
	}
	if got.ResultDecision != ResultFail || got.Status != StatusDone {
		t.Fatalf("result = %q status = %s, want fail recorded and status unchanged (done)", got.ResultDecision, got.Status)
	}
	if _, err := s.SubmitSelfResult(final.ID, SelfResultInput{Decision: ResultPass, Reason: "again"}, final.ID); !errors.Is(err, ErrSelfResultExists) {
		t.Fatalf("second report err = %v, want ErrSelfResultExists", err)
	}
	rec, ok, err := s.meta.GetJob(final.ID)
	if err != nil || !ok {
		t.Fatalf("GetJob: %v %v", ok, err)
	}
	if r := fromRecord(rec); r.ResultDecision != ResultFail || r.ResultReason != "could not finish" || r.ResultAttempt < 1 {
		t.Fatalf("stored result = %+v", r)
	}
	var in SelfResultInput
	if !DecodeSelfResultReport(got.ResultReportJSON, &in) || in.ActionRequired != "look" || len(in.Findings) != 1 {
		t.Fatalf("report document = %q", got.ResultReportJSON)
	}
	if _, err := s.SubmitSelfResult("job-missing", SelfResultInput{Decision: ResultPass, Reason: "x"}, ""); err == nil {
		t.Fatal("unknown job accepted a report")
	}
}
