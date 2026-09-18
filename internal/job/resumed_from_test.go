package job

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestResumedFromRoundTripsRequestJSON pins the wire contract of a continuation
// (SUP-02 R1 / design 决策 2, h-aii-9qiy): resumed_from is a NORMAL, optional field
// of the request JSON — a forwarded (peer / worker) job has to carry it, because
// the executor's acp runner decides session/load vs session/new from it. The
// SECURITY markers stay off the wire (json:"-"): a client that could set
// resume_source_agent would exempt itself from allow_exec, review_fixed would pin
// its own review verdict and todo_foreign would detach a job from the hub's todo.
func TestResumedFromRoundTripsRequestJSON(t *testing.T) {
	req := JobRequest{
		ProjectKey: "self", Agent: "acpbot", Runner: "peer-stub",
		Prompt: "keep going", Cwd: ".", SessionID: "sess-acp-1", ResumedFrom: "job-src-1",
		ResumeSourceAgent: "acpbot", ReviewFixed: true, TodoForeign: true, SourceJobID: "job-src-1",
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	for _, want := range []string{`"resumed_from":"job-src-1"`, `"session_id":"sess-acp-1"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("request JSON %s missing %s", body, want)
		}
	}
	for _, banned := range []string{"resume_source_agent", "review_fixed", "todo_foreign", "source_job_id"} {
		if strings.Contains(string(body), banned) {
			t.Fatalf("request JSON must not carry the internal marker %q: %s", banned, body)
		}
	}

	var back JobRequest
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if back.ResumedFrom != "job-src-1" || back.SessionID != "sess-acp-1" {
		t.Fatalf("round trip lost the continuation: resumed_from=%q session_id=%q", back.ResumedFrom, back.SessionID)
	}
	// What the field is FOR: the executor loads the session only because the request
	// says it is a continuation (a bare session_id binds without replaying history).
	if got := resumeLoadSessionID(back); got != "sess-acp-1" {
		t.Fatalf("resumeLoadSessionID = %q, want the source session", got)
	}

	// A plain job is unchanged byte-for-byte: no lineage field appears at all.
	plain, err := json.Marshal(JobRequest{ProjectKey: "self", Agent: "exec", Cmd: []string{"true"}, SessionID: "sess-x"})
	if err != nil {
		t.Fatalf("marshal plain request: %v", err)
	}
	if strings.Contains(string(plain), "resumed_from") {
		t.Fatalf("a plain job must not carry resumed_from: %s", plain)
	}
	var plainBack JobRequest
	if err := json.Unmarshal(plain, &plainBack); err != nil {
		t.Fatalf("unmarshal plain request: %v", err)
	}
	if got := resumeLoadSessionID(plainBack); got != "" {
		t.Fatalf("a plain job with a session_id must not LOAD it, got %q", got)
	}
}
