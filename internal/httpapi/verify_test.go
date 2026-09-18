package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestSubmitJobVerifyFields: the verify step's submit fields (SUP-01 B) travel the
// HTTP boundary — the body binds straight into job.JobRequest, so the resolved
// request a later rerun replays still carries them — and the terminal state a client
// reads back carries the step's structured result.
func TestSubmitJobVerifyFields(t *testing.T) {
	s := newTestServer(t, testToken, false)
	argv := testcmd.Cmd(t, "exit", "0")

	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		Verify: argv, VerifyTimeoutSec: 25,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatalf("created job has no id: %+v", created)
	}
	final := waitDone(t, s, created.ID)
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Verify == nil || final.Verify.Status != job.VerifyPassed {
		t.Fatalf("job response verify = %+v, want the passed step", final.Verify)
	}
	if len(final.Verify.Command) != len(argv) || final.Verify.Command[0] != argv[0] {
		t.Fatalf("verify command = %v, want the submitted argv %v", final.Verify.Command, argv)
	}

	// The persisted request keeps both fields (it is what a rerun/rebuild replays).
	rr := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/request", testToken, nil)
	if rr.StatusCode != http.StatusOK {
		t.Fatalf("get request status=%d, want 200", rr.StatusCode)
	}
	raw, err := io.ReadAll(rr.Body)
	_ = rr.Body.Close()
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var got job.JobRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if len(got.Verify) != len(argv) || got.Verify[0] != argv[0] {
		t.Fatalf("request verify = %v, want %v", got.Verify, argv)
	}
	if got.VerifyTimeoutSec != 25 {
		t.Fatalf("request verify_timeout_sec = %d, want 25", got.VerifyTimeoutSec)
	}

	// no_verify is the opt-out of the project default; it must survive the boundary
	// too (this project has no default, so the field is what is asserted).
	off := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "0"), Cwd: ".", TimeoutSec: 30,
		NoVerify: true,
	})
	var offCreated job.JobResult
	decode(t, off, &offCreated)
	offReq := do(t, s, http.MethodGet, "/v1/jobs/"+offCreated.ID+"/request", testToken, nil)
	var offGot job.JobRequest
	decode(t, offReq, &offGot)
	if !offGot.NoVerify {
		t.Fatalf("no_verify did not round-trip through the HTTP boundary: %+v", offGot)
	}
	waitDone(t, s, offCreated.ID)
}
