package client

import (
	"net/http"
	"net/url"
)

func (c *Client) DeleteJob(id string) error {
	return c.doJSON(http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), nil, nil)
}
