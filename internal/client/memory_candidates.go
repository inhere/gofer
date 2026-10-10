package client

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// MemoryCandidateListOpts filters GET /v1/memory-candidates (gofer-3nxa.2). Status ""
// = pending, "all" = every state.
type MemoryCandidateListOpts struct {
	JobID   string
	Project string
	Status  string
}

// ListMemoryCandidates reads the knowledge candidates of delivered jobs.
func (c *Client) ListMemoryCandidates(o MemoryCandidateListOpts) ([]jobstore.MemoryCandidate, error) {
	q := url.Values{}
	if o.JobID != "" {
		q.Set("job_id", o.JobID)
	}
	if o.Project != "" {
		q.Set("project", o.Project)
	}
	if o.Status != "" {
		q.Set("status", o.Status)
	}
	path := "/v1/memory-candidates"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Candidates []jobstore.MemoryCandidate `json:"candidates"`
	}
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out.Candidates, err
}

// AcceptMemoryCandidate files candidate id as a scoped memory.
func (c *Client) AcceptMemoryCandidate(id int64, in job.AcceptMemoryCandidateInput) (job.MemoryCandidateDecision, error) {
	body, err := jsonBody(in)
	if err != nil {
		return job.MemoryCandidateDecision{}, err
	}
	var out job.MemoryCandidateDecision
	err = c.doJSON(http.MethodPost, "/v1/memory-candidates/"+strconv.FormatInt(id, 10)+"/accept", body, &out)
	return out, err
}

// RejectMemoryCandidate marks candidate id rejected.
func (c *Client) RejectMemoryCandidate(id int64) (jobstore.MemoryCandidate, error) {
	var out job.MemoryCandidateDecision
	err := c.doJSON(http.MethodPost, "/v1/memory-candidates/"+strconv.FormatInt(id, 10)+"/reject", nil, &out)
	return out.Candidate, err
}
