package mcpserver

// gofer_job_preview_create | list | revoke | renew: the MCP face of the job preview
// control plane (design 2026-10-28-job-preview-design.md §3.4). The tools only bind: the
// lease logic, the permission rules and the error codes live behind the HTTP endpoints,
// which the Backend reaches through internal/client. The one secret is the bearer URL
// create returns: it goes into that single tool result and nowhere else (no log line, no
// error text, no later list).

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
)

type previewCreateInput struct {
	JobID string `json:"job_id"`
	// Port is the TCP port the job's application listens on (on the job's machine, 1..65535).
	Port int `json:"port"`
	// TTLSec is the lease length in seconds; 0 = the server default, capped at its maximum.
	TTLSec int `json:"ttl_sec,omitempty"`
}

type previewJobInput struct {
	JobID string `json:"job_id"`
}

type previewLeaseInput struct {
	JobID     string `json:"job_id"`
	PreviewID string `json:"preview_id"`
}

type previewRenewInput struct {
	JobID     string `json:"job_id"`
	PreviewID string `json:"preview_id"`
	// TTLSec is the new lease length counted from now; 0 = the server default.
	TTLSec int `json:"ttl_sec,omitempty"`
}

// previewsView is the gofer_job_preview_list envelope.
type previewsView struct {
	Previews []client.JobPreview `json:"previews"`
}

// registerPreviewTools registers the four preview tools. They are registered on every
// surface that registers the ordinary job tools: whether the caller may use them is the
// server's decision (a job credential, for one, is refused there).
func registerPreviewTools(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_job_preview_create",
		Description: "Open a short-lived browser preview of a web application a RUNNING job serves on a local port (job_id, port, optional ttl_sec). Returns {id, url, port, target_port, expires_at, worker_id}: url is a one-time bearer URL for a person to open, shown only in this result — do not write it into prompts, comments or files. Only jobs on the server's own local runner are supported; a finished job, a disabled feature or a worker job is refused with an explanation.",
	}, previewCreateHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_job_preview_list",
		Description: "List a job's previews {id, job_id, port, state, created_at, expires_at, close_reason} (never the URL or token).",
	}, previewListHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_job_preview_revoke",
		Description: "Revoke a job's preview by preview_id: the lease closes and its open connections are cut. You may revoke your own previews; someone else's needs the job's owner or job.manage.others.",
	}, previewRevokeHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_job_preview_renew",
		Description: "Renew an active preview of a job: the expiry becomes now + ttl_sec (capped at the server maximum). Returns {id, state, expires_at, port}; an expired or revoked preview is refused — create a new one.",
	}, previewRenewHandler(b))
}

func previewCreateHandler(b Backend) mcp.ToolHandlerFor[previewCreateInput, client.JobPreview] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in previewCreateInput) (*mcp.CallToolResult, client.JobPreview, error) {
		jobID := strings.TrimSpace(in.JobID)
		if jobID == "" {
			return nil, client.JobPreview{}, errors.New(text(b, "mcp.error.preview_job_required"))
		}
		if in.Port < 1 || in.Port > 65535 {
			return nil, client.JobPreview{}, errors.New(text(b, "mcp.error.preview_port_invalid"))
		}
		if in.TTLSec < 0 {
			return nil, client.JobPreview{}, errors.New(text(b, "mcp.error.preview_ttl_invalid"))
		}
		out, err := b.CreateJobPreview(jobID, in.Port, in.TTLSec)
		if err != nil {
			return nil, client.JobPreview{}, previewError(b, err)
		}
		if out.JobID == "" {
			out.JobID = jobID
		}
		return nil, out, nil
	}
}

func previewListHandler(b Backend) mcp.ToolHandlerFor[previewJobInput, previewsView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in previewJobInput) (*mcp.CallToolResult, previewsView, error) {
		jobID := strings.TrimSpace(in.JobID)
		if jobID == "" {
			return nil, previewsView{}, errors.New(text(b, "mcp.error.preview_job_required"))
		}
		rows, err := b.ListJobPreviews(jobID)
		if err != nil {
			return nil, previewsView{}, previewError(b, err)
		}
		if rows == nil {
			rows = []client.JobPreview{}
		}
		return nil, previewsView{Previews: rows}, nil
	}
}

func previewRevokeHandler(b Backend) mcp.ToolHandlerFor[previewLeaseInput, client.JobPreview] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in previewLeaseInput) (*mcp.CallToolResult, client.JobPreview, error) {
		jobID, id := strings.TrimSpace(in.JobID), strings.TrimSpace(in.PreviewID)
		if jobID == "" || id == "" {
			return nil, client.JobPreview{}, errors.New(text(b, "mcp.error.preview_ids_required"))
		}
		out, err := b.CloseJobPreview(jobID, id)
		if err != nil {
			return nil, client.JobPreview{}, previewError(b, err)
		}
		return nil, out, nil
	}
}

func previewRenewHandler(b Backend) mcp.ToolHandlerFor[previewRenewInput, client.JobPreview] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in previewRenewInput) (*mcp.CallToolResult, client.JobPreview, error) {
		jobID, id := strings.TrimSpace(in.JobID), strings.TrimSpace(in.PreviewID)
		if jobID == "" || id == "" {
			return nil, client.JobPreview{}, errors.New(text(b, "mcp.error.preview_ids_required"))
		}
		if in.TTLSec < 0 {
			return nil, client.JobPreview{}, errors.New(text(b, "mcp.error.preview_ttl_invalid"))
		}
		out, err := b.RenewJobPreview(jobID, id, in.TTLSec)
		if err != nil {
			return nil, client.JobPreview{}, previewError(b, err)
		}
		return nil, out, nil
	}
}

// previewError words a server refusal in the MCP's own language: an error that carries a
// stable code the err.* catalog knows is rendered from that catalog (the server's text is
// in its own language, which is not necessarily the agent's); anything else keeps the
// message the client built. The result never contains a URL or token — none is in an error
// body.
func previewError(b Backend, err error) error {
	var se *client.StatusError
	if errors.As(err, &se) && se.Code != "" {
		lang := backendLang(b)
		if key := "err." + se.Code; i18n.HasKey(lang, key) {
			return fmt.Errorf("%s", i18n.T(lang, key, se.Params))
		}
	}
	return err
}
