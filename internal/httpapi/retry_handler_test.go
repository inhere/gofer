package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// retryJSON is the wire shape of one retry row, decoded separately from the handler's
// own view type: the test then pins the CONTRACT a client sees (field names, omission)
// rather than restating the server struct.
type retryJSON struct {
	ID          string `json:"id"`
	SourceJobID string `json:"source_job_id"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
	Reason      string `json:"reason"`
	NextRunAt   int64  `json:"next_run_at"`
	LeaseUntil  int64  `json:"lease_until"`
	State       string `json:"state"`
	NewJobID    string `json:"new_job_id"`
	CreatedAt   int64  `json:"created_at"`
}

// TestListRetriesUnknownJob: AUTO-03 的读端点对未知 job 是 404，不是空列表——一条空链和
// "这个 id 打错了"必须分得开（与 handleListWakeups 同款判断）。
func TestListRetriesUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/job-does-not-exist/retries", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 for an unknown job", resp.StatusCode)
	}
	var body map[string]any
	decode(t, resp, &body)
	if body["error"] != "unknown job" {
		t.Fatalf("error=%v, want \"unknown job\"", body["error"])
	}
}

// TestListRetriesAfterFailure is the end-to-end contract: a job that fails with a
// retry policy leaves ONE pending row for its next attempt, and the endpoint reports
// it with the attempt ceiling read back off the row itself.
func TestListRetriesAfterFailure(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodPost, "/v1/jobs", testToken, job.JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: testcmd.Cmd(t, "exit", "7"), Cwd: ".", TimeoutSec: 30,
		// 30s 退避：这一行必须留在 pending（本测试不跑 sweeper），所以下一档不能已经到期。
		Retry: &job.RetryPolicy{MaxAttempts: 3, BackoffSec: []int{30}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d, want 200", resp.StatusCode)
	}
	var created job.JobResult
	decode(t, resp, &created)

	final := waitDoneTok(t, s, created.ID, testToken)
	if final.Status != job.StatusFailed || final.ExitCode != 7 {
		t.Fatalf("job status=%s exit=%d, want failed/7 (fixture: testcmd exit 7)", final.Status, final.ExitCode)
	}

	// The row is written by the failing job's finish path, right after the terminal
	// status is durable — poll briefly so the assertion is about the contract, not
	// about winning that microsecond race.
	rows := pollRetries(t, s, created.ID)
	if len(rows) != 1 {
		t.Fatalf("retries=%d, want exactly 1 row for a failed attempt 1 of 3", len(rows))
	}
	got := rows[0]
	if got.ID == "" {
		t.Fatalf("retry row has no id: %+v", got)
	}
	if got.SourceJobID != created.ID {
		t.Fatalf("source_job_id=%q, want %q", got.SourceJobID, created.ID)
	}
	if got.Attempt != 2 {
		t.Fatalf("attempt=%d, want 2 (the re-run of attempt 1)", got.Attempt)
	}
	if got.MaxAttempts != 3 {
		t.Fatalf("max_attempts=%d, want 3 (the policy the row was scheduled under)", got.MaxAttempts)
	}
	if got.State != jobstore.RetryPending {
		t.Fatalf("state=%q, want pending", got.State)
	}
	if got.Reason != "exit_code=7" {
		t.Fatalf("reason=%q, want exit_code=7", got.Reason)
	}
	if got.NextRunAt <= time.Now().Unix()-5 {
		t.Fatalf("next_run_at=%d, want a future-ish instant", got.NextRunAt)
	}
	// A pending row has no lease and no submitted job: both are omitted (Go zero).
	if got.LeaseUntil != 0 || got.NewJobID != "" {
		t.Fatalf("pending row carries lease_until=%d new_job_id=%q, want both unset", got.LeaseUntil, got.NewJobID)
	}

	// cancel: the retry is dropped and a second cancel is the 404 a terminal row gets.
	del := do(t, s, http.MethodDelete, "/v1/retries/"+got.ID, testToken, nil)
	if del.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d, want 200", del.StatusCode)
	}
	var cancelled map[string]string
	decode(t, del, &cancelled)
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancel body=%v, want status=cancelled", cancelled)
	}
	again := do(t, s, http.MethodDelete, "/v1/retries/"+got.ID, testToken, nil)
	if again.StatusCode != http.StatusNotFound {
		t.Fatalf("second cancel status=%d, want 404 (the row is terminal)", again.StatusCode)
	}
	again.Body.Close()

	if after := pollRetries(t, s, created.ID); len(after) != 1 || after[0].State != jobstore.RetryCancelled {
		t.Fatalf("after cancel: %+v, want the single row cancelled", after)
	}
}

// TestCancelRetryUnknown: 取消一个不存在的重试 id 就是 404，detail 用服务端原文
// （不伪造一个"已取消"）。既有的 delete-wakeup 也是这么映射的。
func TestCancelRetryUnknown(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodDelete, "/v1/retries/rt-nope", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	var body map[string]any
	decode(t, resp, &body)
	if body["detail"] == nil || body["detail"] == "" {
		t.Fatalf("detail=%v, want the store's own message", body["detail"])
	}
}

// pollRetries reads GET /v1/jobs/{id}/retries until it reports at least one row (the
// failing job writes the row just after its terminal status lands) and returns the
// decoded chain.
func pollRetries(t *testing.T, s *Server, jobID string) []retryJSON {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := do(t, s, http.MethodGet, "/v1/jobs/"+jobID+"/retries", testToken, nil)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("list retries status=%d, want 200", resp.StatusCode)
		}
		var out struct {
			JobID   string      `json:"job_id"`
			Retries []retryJSON `json:"retries"`
		}
		decode(t, resp, &out)
		if out.JobID != jobID {
			t.Fatalf("job_id=%q, want %q", out.JobID, jobID)
		}
		if len(out.Retries) > 0 || time.Now().After(deadline) {
			return out.Retries
		}
		time.Sleep(20 * time.Millisecond)
	}
}
