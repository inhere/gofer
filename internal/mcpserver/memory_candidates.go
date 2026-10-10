package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Knowledge candidates (gofer-3nxa.2): the 「## 可复用经验」 items of delivered jobs. The
// tools list them and record a decision; adopting (HTTP / CLI "accept") writes a scoped
// memory. The verb is "adopt" on purpose: the MCP surface carries no "accept" tool
// (TestNoAcceptJobTool — an agent never signs off work). A job credential may only
// list — adopt / reject are refused for it by the server.

type memoryCandidatesInput struct {
	// JobID limits the list to one job's candidates.
	JobID string `json:"job_id,omitempty"`
	// Project limits the list to one project's candidates.
	Project string `json:"project,omitempty"`
	// Status is pending (default) | accepted | rejected | all.
	Status string `json:"status,omitempty"`
}

type memoryCandidateDecideInput struct {
	// ID is the candidate id (from gofer_memory_candidates).
	ID int64 `json:"id"`
	// Key is the memory key (accept only, required).
	Key string `json:"key,omitempty"`
	// Kind is rule | note (accept only; default note).
	Kind string `json:"kind,omitempty"`
	// Summary is a one-sentence summary (accept only; required for content > 200 chars).
	Summary string `json:"summary,omitempty"`
	// Global writes to the global scope instead of the job's project (accept only).
	Global bool `json:"global,omitempty"`
	// Project writes to this project's scope instead of the job's project (accept only).
	Project string `json:"project,omitempty"`
}

func registerMemoryCandidateTools(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_memory_candidates",
		Description: "List knowledge candidates: the 「## 可复用经验」 report items of delivered jobs, waiting for a person to accept (it becomes a server memory, source job:<id>) or reject. Filters: job_id, project, status (pending default | accepted | rejected | all).",
	}, memoryCandidatesHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_memory_candidate_adopt",
		Description: "Adopt (accept) a knowledge candidate on a person's behalf: write its text as a server memory under key (kind rule|note, default note; summary required for content > 200 chars) in the job's project scope (or global=true / project=<key>). An existing key is refused. Job credentials are refused.",
	}, memoryCandidateDecideHandler(b, true))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_memory_candidate_reject",
		Description: "Reject a knowledge candidate (nothing is written). Job credentials are refused.",
	}, memoryCandidateDecideHandler(b, false))
}

func memoryCandidatesHandler(b Backend) mcp.ToolHandlerFor[memoryCandidatesInput, map[string]any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in memoryCandidatesInput) (*mcp.CallToolResult, map[string]any, error) {
		rows, err := b.ListMemoryCandidates(jobstore.MemoryCandidateFilter{
			JobID: strings.TrimSpace(in.JobID), ProjectKey: strings.TrimSpace(in.Project), Status: strings.TrimSpace(in.Status),
		})
		if err != nil {
			return nil, map[string]any{}, err
		}
		return nil, map[string]any{"candidates": rows}, nil
	}
}

func memoryCandidateDecideHandler(b Backend, accept bool) mcp.ToolHandlerFor[memoryCandidateDecideInput, job.MemoryCandidateDecision] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in memoryCandidateDecideInput) (*mcp.CallToolResult, job.MemoryCandidateDecision, error) {
		if in.ID <= 0 {
			return nil, job.MemoryCandidateDecision{}, fmt.Errorf("id is required")
		}
		if !accept {
			cand, err := b.RejectMemoryCandidate(in.ID)
			return nil, job.MemoryCandidateDecision{Candidate: cand}, err
		}
		if strings.TrimSpace(in.Key) == "" {
			return nil, job.MemoryCandidateDecision{}, fmt.Errorf("key is required")
		}
		out, err := b.AcceptMemoryCandidate(in.ID, job.AcceptMemoryCandidateInput{
			Key: in.Key, Kind: in.Kind, Summary: in.Summary, Global: in.Global, Project: in.Project,
		})
		return nil, out, err
	}
}

func (b *clientBackend) ListMemoryCandidates(f jobstore.MemoryCandidateFilter) ([]jobstore.MemoryCandidate, error) {
	return b.cli.ListMemoryCandidates(client.MemoryCandidateListOpts{JobID: f.JobID, Project: f.ProjectKey, Status: f.Status})
}

func (b *clientBackend) AcceptMemoryCandidate(id int64, in job.AcceptMemoryCandidateInput) (job.MemoryCandidateDecision, error) {
	return b.cli.AcceptMemoryCandidate(id, in)
}

func (b *clientBackend) RejectMemoryCandidate(id int64) (jobstore.MemoryCandidate, error) {
	return b.cli.RejectMemoryCandidate(id)
}

func (b *localBackend) ListMemoryCandidates(f jobstore.MemoryCandidateFilter) ([]jobstore.MemoryCandidate, error) {
	return b.jobs.ListMemoryCandidates(f)
}

func (b *localBackend) AcceptMemoryCandidate(id int64, in job.AcceptMemoryCandidateInput) (job.MemoryCandidateDecision, error) {
	return b.jobs.AcceptMemoryCandidate(id, in, "mcp")
}

func (b *localBackend) RejectMemoryCandidate(id int64) (jobstore.MemoryCandidate, error) {
	return b.jobs.RejectMemoryCandidate(id, "mcp")
}
