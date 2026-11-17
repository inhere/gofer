package client

import (
	"net/http"
	"net/url"

	"github.com/inhere/gofer/internal/job"
)

// GetPlanDriverReport reads a plan's driver report (GET /v1/plans/{id}/driver/report): the
// recorded actions against what actually happened, per action and per rule.
func (c *Client) GetPlanDriverReport(planID string) (job.PlanDriverReport, error) {
	var rep job.PlanDriverReport
	err := c.doJSON(http.MethodGet, "/v1/plans/"+url.PathEscape(planID)+"/driver/report", nil, &rep)
	return rep, err
}
