package client

import (
	"net/http"

	"github.com/inhere/gofer/internal/today"
)

// Today reads GET /v1/today (the 「今天」 decision queue, digest and status bar).
func (c *Client) Today(includeExec bool) (today.Response, error) {
	path := "/v1/today"
	if includeExec {
		path += "?include_exec=1"
	}
	var out today.Response
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out, err
}

// TodayAdvise writes the steward's (or a person's) advice on one 「今天」 card.
func (c *Client) TodayAdvise(in today.AdviceInput) (today.Advice, error) {
	body, err := jsonBody(in)
	if err != nil {
		return today.Advice{}, err
	}
	var out struct {
		Advice today.Advice `json:"advice"`
	}
	err = c.doJSON(http.MethodPost, "/v1/today/advice", body, &out)
	return out.Advice, err
}
