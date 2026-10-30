package client

import (
	"net/http"
	"net/url"
)

// Ref is one source object that references a requested destination.
type Ref struct {
	SourceKind  string `json:"source_kind"`
	SourceID    string `json:"source_id"`
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Field       string `json:"field"`
	CreatedAt   int64  `json:"created_at"`
	SourceTitle string `json:"source_title,omitempty"`
}

// ListRefs returns visible sources that reference kind/id.
func (c *Client) ListRefs(kind, id string) ([]Ref, error) {
	q := url.Values{}
	q.Set("kind", kind)
	q.Set("id", id)
	var out struct {
		Refs []Ref `json:"refs"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/refs?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	return out.Refs, nil
}
