package client

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/inhere/gofer/internal/stage"
)

// The flow surface of `gofer stage` (plan 2026-11-26-flow-progression-plan.md, F2). The
// shapes are internal/stage's own: the views and results are what the server answers, so
// the CLI and the handler cannot drift apart.

// stagePath is /v1/work-items/{id}/stage[/rest].
func stagePath(id string, rest ...string) string {
	p := "/v1/work-items/" + url.PathEscape(id) + "/stage"
	for _, r := range rest {
		p += "/" + r
	}
	return p
}

// StageStartOn starts a flow on a work item, linking issue to it when given. template is a
// built-in name (feature | small) or "" for the server default; sessionID, when set,
// becomes a current session of the item.
func (c *Client) StageStartOn(workItemID, issue, template, sessionID string) (stage.View, error) {
	fields := map[string]any{"issue": issue}
	for k, v := range map[string]string{"template": template, "session_id": sessionID} {
		if v != "" {
			fields[k] = v
		}
	}
	body, err := jsonBody(fields)
	if err != nil {
		return stage.View{}, err
	}
	var out stage.View
	err = c.doJSON(http.MethodPost, stagePath(workItemID), body, &out)
	return out, err
}

// StageStartByIssue starts a flow by issue: the open work item already linked to it, else
// a new item linked to it. projectKey / title are used only when the item is created.
func (c *Client) StageStartByIssue(issue, template, projectKey, title, sessionID string) (stage.View, error) {
	fields := map[string]any{"issue": issue}
	for k, v := range map[string]string{"template": template, "project": projectKey, "title": title, "session_id": sessionID} {
		if v != "" {
			fields[k] = v
		}
	}
	body, err := jsonBody(fields)
	if err != nil {
		return stage.View{}, err
	}
	var out stage.View
	err = c.doJSON(http.MethodPost, "/v1/work-items/stage", body, &out)
	return out, err
}

// GetStage reads a work item's flow with the current step's gate evaluated.
func (c *Client) GetStage(workItemID string) (stage.View, error) {
	var out stage.View
	err := c.doJSON(http.MethodGet, stagePath(workItemID), nil, &out)
	return out, err
}

// StageDone asks the server to evaluate the current step's gate and move on when it passes.
func (c *Client) StageDone(workItemID string) (stage.Result, error) {
	var out stage.Result
	err := c.doJSON(http.MethodPost, stagePath(workItemID, "done"), nil, &out)
	return out, err
}

// StageBack sends the flow one step back; a reason is required.
func (c *Client) StageBack(workItemID, reason string) (stage.View, error) {
	return c.stageWithReason(workItemID, "back", reason)
}

// StageBlocked marks the flow blocked on its current step; a reason is required.
func (c *Client) StageBlocked(workItemID, reason string) (stage.View, error) {
	return c.stageWithReason(workItemID, "blocked", reason)
}

// StageApprove records a person's approval of the current step (the server refuses a job
// credential and a request that names a live session).
func (c *Client) StageApprove(workItemID string) (stage.Result, error) {
	var out stage.Result
	err := c.doJSON(http.MethodPost, stagePath(workItemID, "approve"), nil, &out)
	return out, err
}

// StageDrop abandons the flow (the work item is untouched); the reason is optional.
func (c *Client) StageDrop(workItemID, reason string) (stage.View, error) {
	return c.stageWithReason(workItemID, "drop", reason)
}

func (c *Client) stageWithReason(workItemID, action, reason string) (stage.View, error) {
	body, err := jsonBody(map[string]any{"reason": strings.TrimSpace(reason)})
	if err != nil {
		return stage.View{}, err
	}
	var out stage.View
	err = c.doJSON(http.MethodPost, stagePath(workItemID, action), body, &out)
	return out, err
}

// ListOpenStages lists the flows in progress (project "" = every project the caller sees).
func (c *Client) ListOpenStages(projectKey string) ([]stage.View, error) {
	path := "/v1/work-items/stages"
	if k := strings.TrimSpace(projectKey); k != "" {
		q := url.Values{}
		q.Set("project", k)
		path += "?" + q.Encode()
	}
	var out struct {
		Stages []stage.View `json:"stages"`
	}
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out.Stages, err
}
