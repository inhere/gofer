package client

import (
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/streaming"
)

// GetJobTranscript reads one page of a job's per-turn transcript
// (GET /v1/jobs/{id}/transcript, gofer-mmh0). The response types are the streaming
// package's own: the server and its clients share one definition. A page with Next set
// continues with q.Cursor = Next.String().
func (c *Client) GetJobTranscript(id string, q streaming.TranscriptQuery) (streaming.JobTranscriptPage, error) {
	v := url.Values{}
	if q.Outline {
		v.Set("outline", "1")
	}
	for k, val := range map[string]string{"turns": q.Turns, "head": q.Head, "tail": q.Tail, "search": q.Search, "cursor": q.Cursor} {
		if val != "" {
			v.Set(k, val)
		}
	}
	path := "/v1/jobs/" + url.PathEscape(id) + "/transcript"
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	var page streaming.JobTranscriptPage
	err := c.doJSON(http.MethodGet, path, nil, &page)
	return page, err
}
