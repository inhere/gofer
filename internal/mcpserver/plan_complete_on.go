package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/jobstore"
)

type setPlanCompleteOnToolInput struct {
	PlanID string `json:"plan_id"`
	// CompleteOn is job|merge: when this plan's todos count as done.
	CompleteOn string `json:"complete_on"`
}

// setPlanCompleteOn validates the value (the HTTP entrance and jobstore check again)
// and forwards it to the backend.
func setPlanCompleteOn(b Backend, planID, completeOn string) (planView, error) {
	completeOn = strings.TrimSpace(completeOn)
	if !jobstore.ValidPlanCompleteOn(completeOn) {
		return planView{}, fmt.Errorf("complete_on must be job or merge")
	}
	return b.SetPlanCompleteOn(planID, completeOn)
}

func setPlanCompleteOnHandler(b Backend) mcp.ToolHandlerFor[setPlanCompleteOnToolInput, planView] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in setPlanCompleteOnToolInput) (*mcp.CallToolResult, planView, error) {
		if in.PlanID == "" {
			return nil, planView{}, fmt.Errorf("plan_id is required")
		}
		pv, err := setPlanCompleteOn(b, in.PlanID, in.CompleteOn)
		if err != nil {
			return nil, planView{}, err
		}
		return nil, pv, nil
	}
}
