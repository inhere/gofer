package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/today"
)

// P4 memory hygiene: the steward may read the server-side memory doctor over the repo-tracker
// mirror. It is a read; cleanup itself is not a steward job (settled memories are archived
// automatically, the rest is a person's or the working agent's call).

type memoryFindingsInput struct {
	// TrackerID limits the findings to one mirrored repository.
	TrackerID string `json:"tracker_id,omitempty"`
}

type memoryFindingsOutput struct {
	Findings []today.MemoryFinding `json:"findings"`
}

func registerMemoryHygieneTools(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name:        "gofer_memory_findings",
		Description: "Read the server-side memory doctor over the mirrored repository tracker memories (read-only): per flagged memory its tracker_id, key, kind, summary, age, findings (flagged | handoff-expired | note-stale | summary-missing | duplicate; path / commit checks need a checkout and are skipped) and the content. Filter: tracker_id.",
	}, memoryFindingsHandler(b))
}

func memoryFindingsHandler(b Backend) mcp.ToolHandlerFor[memoryFindingsInput, memoryFindingsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in memoryFindingsInput) (*mcp.CallToolResult, memoryFindingsOutput, error) {
		items, err := b.MemoryFindings(strings.TrimSpace(in.TrackerID))
		if err != nil {
			return nil, memoryFindingsOutput{}, err
		}
		if items == nil {
			items = []today.MemoryFinding{}
		}
		return nil, memoryFindingsOutput{Findings: items}, nil
	}
}

func (b *clientBackend) MemoryFindings(trackerID string) ([]today.MemoryFinding, error) {
	return b.cli.MemoryFindings(trackerID)
}

func (b *localBackend) MemoryFindings(string) ([]today.MemoryFinding, error) {
	return nil, errNeedsServer
}
