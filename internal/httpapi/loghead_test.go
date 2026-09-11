package httpapi

import (
	"io"
	"net/http"
	"testing"
)

func TestLogHeadWindow(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createDoneExecJob(t, s)
	writeStdoutLog(t, created.ResultDir, numberedLines(205))

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/logs/stdout?head=1&lines=3", testToken, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got, want := string(body), numberedRange(1, 3); got != want {
		t.Fatalf("head body=%q want %q", got, want)
	}
	if got, want := resp.Header.Get("X-Log-Total-Lines"), "205"; got != want {
		t.Fatalf("X-Log-Total-Lines=%q want %q", got, want)
	}
	if got, want := resp.Header.Get("X-Log-Lines"), "3"; got != want {
		t.Fatalf("X-Log-Lines=%q want %q", got, want)
	}
}

func TestLogHeadRejectsOffset(t *testing.T) {
	s := newTestServer(t, testToken, false)
	created := createDoneExecJob(t, s)
	writeStdoutLog(t, created.ResultDir, numberedLines(10))

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+created.ID+"/logs/stdout?head=1&lines=3&offset=2", testToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("head+offset status=%d, want 400", resp.StatusCode)
	}
}
