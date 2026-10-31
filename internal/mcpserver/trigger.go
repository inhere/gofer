package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/trigger"
)

// Read-only inbound-trigger tools (design 2026-10-28-trigger-design.md 3.4). They show the
// same redacted views as GET /v1/triggers and need the trigger.read permission, which the
// server enforces on the HTTP call underneath (a job credential is refused there). There is
// deliberately no tool that invokes a trigger or changes one: an agent never holds a trigger
// secret, and its own job submit keeps its own credential's limits.

type triggerListOutput struct {
	Triggers []trigger.View `json:"triggers"`
}

type triggerShowInput struct {
	Name string `json:"name"`
	// Deliveries is how many of the newest deliveries to include (default 5, 0 = none).
	Deliveries *int `json:"deliveries,omitempty"`
}

type triggerShowOutput struct {
	Trigger    trigger.View           `json:"trigger"`
	Deliveries []trigger.DeliveryView `json:"deliveries"`
}

const defaultTriggerShowDeliveries = 5

func registerTriggerTools(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_trigger_list",
		Description: "List the server's inbound triggers (redacted: owner, project, task book, limits and newest delivery; the secret is shown only as its variable name and whether it is set). Needs the trigger.read permission; a job credential cannot use it.",
	}, triggerListHandler(b))
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_trigger_show",
		Description: "Show one inbound trigger and its newest deliveries (event ids only as short hashes, never a payload). Needs the trigger.read permission; a job credential cannot use it.",
	}, triggerShowHandler(b))
}

func triggerListHandler(b Backend) mcp.ToolHandlerFor[struct{}, triggerListOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, triggerListOutput, error) {
		views, err := b.ListTriggers()
		if err != nil {
			return nil, triggerListOutput{}, err
		}
		if views == nil {
			views = []trigger.View{}
		}
		return nil, triggerListOutput{Triggers: views}, nil
	}
}

func triggerShowHandler(b Backend) mcp.ToolHandlerFor[triggerShowInput, triggerShowOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in triggerShowInput) (*mcp.CallToolResult, triggerShowOutput, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, triggerShowOutput{}, fmt.Errorf("name is required")
		}
		limit := defaultTriggerShowDeliveries
		if in.Deliveries != nil {
			limit = *in.Deliveries
		}
		v, err := b.GetTrigger(name)
		if err != nil {
			return nil, triggerShowOutput{}, err
		}
		out := triggerShowOutput{Trigger: v, Deliveries: []trigger.DeliveryView{}}
		if limit > 0 {
			rows, err := b.TriggerDeliveries(name, limit)
			if err != nil {
				return nil, triggerShowOutput{}, err
			}
			if rows != nil {
				out.Deliveries = rows
			}
		}
		return nil, out, nil
	}
}
