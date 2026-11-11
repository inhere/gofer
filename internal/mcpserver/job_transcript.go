package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/streaming"
)

// --- gofer_get_job_transcript ----------------------------------------------

// transcriptInput mirrors `gofer job transcript` / GET /v1/jobs/{id}/transcript: at most
// one of outline / turns / head / tail / search (none = outline), cursor to continue.
type transcriptInput struct {
	ID      string `json:"id"`
	Outline bool   `json:"outline,omitempty"`
	Turns   string `json:"turns,omitempty"`  // "A-B" (or "A"): full text of those turns
	Head    int    `json:"head,omitempty"`   // the first N turns
	Tail    int    `json:"tail,omitempty"`   // the last N turns
	Search  string `json:"search,omitempty"` // literal substring (<= 200 chars)
	Cursor  string `json:"cursor,omitempty"` // "turn:offset" from a previous page's next
}

// transcriptQuery turns the typed input into the shared string query, so MCP, HTTP and
// the CLI validate through one function (streaming.ParseTranscriptQuery). A zero head /
// tail means "not given".
func transcriptQuery(in transcriptInput) streaming.TranscriptQuery {
	q := streaming.TranscriptQuery{Outline: in.Outline, Turns: in.Turns, Search: in.Search, Cursor: in.Cursor}
	if in.Head != 0 {
		q.Head = fmt.Sprint(in.Head)
	}
	if in.Tail != 0 {
		q.Tail = fmt.Sprint(in.Tail)
	}
	return q
}

func jobTranscriptHandler(b Backend) mcp.ToolHandlerFor[transcriptInput, streaming.JobTranscriptPage] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in transcriptInput) (*mcp.CallToolResult, streaming.JobTranscriptPage, error) {
		if in.ID == "" {
			return nil, streaming.JobTranscriptPage{}, errors.New("id is required")
		}
		page, err := b.GetJobTranscript(in.ID, transcriptQuery(in))
		if err != nil {
			return nil, streaming.JobTranscriptPage{}, err
		}
		return nil, page, nil
	}
}

func (b *localBackend) GetJobTranscript(id string, q streaming.TranscriptQuery) (streaming.JobTranscriptPage, error) {
	opts, err := streaming.ParseTranscriptQuery(q)
	if err != nil {
		return streaming.JobTranscriptPage{}, err
	}
	res, ok := b.jobs.Get(id)
	if !ok {
		return streaming.JobTranscriptPage{}, fmt.Errorf("unknown job %q", id)
	}
	return streaming.JobTranscript(res, opts)
}

func (b *clientBackend) GetJobTranscript(id string, q streaming.TranscriptQuery) (streaming.JobTranscriptPage, error) {
	return b.cli.GetJobTranscript(id, q)
}
