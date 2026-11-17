package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
)

// --- gofer_submit_result ----------------------------------------------------

// submitResultDescription is the tool's description, shared by the full and the leader
// surface (the steward surface does not register it).
const submitResultDescription = "Report how THIS job ended, once: decision pass (done as asked), rework (a follow-up pass is needed), fail (could not do it) or human (a person has to decide; say what in action_required), plus a one-sentence reason and optional findings. It is recorded and shown with the job; it does not change the job's status. A second report for the same job is refused."

type submitResultInput struct {
	JobID          string   `json:"job_id,omitempty"`
	Decision       string   `json:"decision"`
	Reason         string   `json:"reason"`
	ActionRequired string   `json:"action_required,omitempty"`
	Findings       []string `json:"findings,omitempty"`
}

type submitResultOutput struct {
	JobID    string `json:"job_id"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	Attempt  int    `json:"attempt"`
}

// registerSubmitResultTool adds gofer_submit_result; both the full surface and the leader
// surface call it.
func registerSubmitResultTool(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_submit_result",
		Description: submitResultDescription,
	}, submitResultHandler(b))
}

// submitResultHandler binds the report to the job this process runs in (GOFER_JOB_ID,
// set by the job service); job_id, when given, must name that same job.
func submitResultHandler(b Backend) mcp.ToolHandlerFor[submitResultInput, submitResultOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in submitResultInput) (*mcp.CallToolResult, submitResultOutput, error) {
		own := strings.TrimSpace(os.Getenv(envJobID))
		id := strings.TrimSpace(in.JobID)
		if id == "" {
			id = own
		}
		if id == "" {
			return nil, submitResultOutput{}, fmt.Errorf("job_id is required when %s is unset: only a job reports its own result", envJobID)
		}
		if own != "" && own != id {
			return nil, submitResultOutput{}, fmt.Errorf("a job may only report its own result")
		}
		res, err := b.SubmitJobResult(id, job.SelfResultInput{
			Decision: in.Decision, Reason: in.Reason, ActionRequired: in.ActionRequired, Findings: in.Findings,
		})
		if err != nil {
			return nil, submitResultOutput{}, err
		}
		return nil, submitResultOutput{JobID: id, Decision: res.ResultDecision, Reason: res.ResultReason, Attempt: res.ResultAttempt}, nil
	}
}
