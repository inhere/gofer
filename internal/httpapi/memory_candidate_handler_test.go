package httpapi

import (
	"net/http"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestMemoryCandidateEndpoints(t *testing.T) {
	s := newTestServer(t, testToken, false)
	meta := s.jobs.Meta()
	_, err := meta.AddMemoryCandidates("job-k1", "self", []string{"lesson one", "lesson two"})
	assert.NoErr(t, err)

	var list struct {
		Candidates []jobstore.MemoryCandidate `json:"candidates"`
	}
	member := seedJobToken(t, s, "job-k-member", jobstore.JobCredentialMember, "")
	todayCall(t, s, http.MethodGet, "/v1/memory-candidates?job_id=job-k1", member, nil, 200, &list)
	assert.Len(t, list.Candidates, 2)
	one, two := "/v1/memory-candidates/"+jsonNum(list.Candidates[0].ID), "/v1/memory-candidates/"+jsonNum(list.Candidates[1].ID)

	// Deciding is a person's.
	todayCall(t, s, http.MethodPost, one+"/accept", member, map[string]any{"key": "k1"}, 403, nil)
	todayCall(t, s, http.MethodPost, one+"/reject", member, nil, 403, nil)

	todayCall(t, s, http.MethodPost, one+"/accept", testToken, map[string]any{}, 400, nil)
	var out struct {
		Candidate jobstore.MemoryCandidate `json:"candidate"`
		Memory    jobstore.ScopedMemory    `json:"memory"`
	}
	todayCall(t, s, http.MethodPost, one+"/accept", testToken, map[string]any{"key": "k1", "kind": "rule"}, 200, &out)
	assert.Eq(t, jobstore.MemoryCandidateAccepted, out.Candidate.Status)
	assert.Eq(t, "self", out.Memory.ScopeKey)
	assert.Eq(t, "job:job-k1", out.Memory.Source)
	todayCall(t, s, http.MethodPost, one+"/accept", testToken, map[string]any{"key": "k1b"}, 409, nil)
	todayCall(t, s, http.MethodPost, two+"/accept", testToken, map[string]any{"key": "k1"}, 409, nil)
	todayCall(t, s, http.MethodPost, two+"/reject", testToken, nil, 200, nil)
	todayCall(t, s, http.MethodPost, "/v1/memory-candidates/999/reject", testToken, nil, 404, nil)

	todayCall(t, s, http.MethodGet, "/v1/memory-candidates", testToken, nil, 200, &list)
	assert.Len(t, list.Candidates, 0)
	todayCall(t, s, http.MethodGet, "/v1/memory-candidates?status=all&project=self", testToken, nil, 200, &list)
	assert.Len(t, list.Candidates, 2)
}
