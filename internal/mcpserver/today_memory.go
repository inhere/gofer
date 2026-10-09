package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/today"
)

// P4 memory hygiene (design prime-memory-quality §2.6): the steward reads the server-side
// memory doctor over the repo-tracker mirror and proposes ONE cleanup per call. It never
// changes a memory: the proposal becomes a 「今天」 card the person adopts or dismisses.

type memoryFindingsInput struct {
	// TrackerID limits the findings to one mirrored repository.
	TrackerID string `json:"tracker_id,omitempty"`
	// All also lists findings whose actions are already pending or were dismissed recently.
	All bool `json:"all,omitempty"`
}

type memoryFindingsOutput struct {
	Findings []today.MemoryFinding `json:"findings"`
	DailyCap int                   `json:"daily_cap"`
}

type memorySuggestOutput struct {
	Suggestion today.MemorySuggestion `json:"suggestion"`
	Recorded   bool                   `json:"recorded"`
	Note       string                 `json:"note"`
}

func registerMemoryHygieneTools(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_memory_findings",
		Description: "Read the server-side memory doctor over the mirrored repository tracker memories (read-only): per flagged memory its tracker_id, key, kind, summary, age, findings (handoff-expired | note-stale | summary-missing | duplicate; path / commit checks need a checkout and are skipped), the still-open cleanup actions (archive | summary | merge) and the content. Filters: tracker_id, all (also findings already proposed or dismissed recently).",
	}, memoryFindingsHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_memory_suggest",
		Description: "Propose ONE cleanup of one repository tracker memory as a 「今天」 card the person adopts (「采纳」) or dismisses (「忽略」); nothing changes until a person adopts it. action: archive | merge (payload.into = target key; optional payload.content = merged body for the target) | kind (payload.kind = rule|note|handoff) | summary (payload.summary <=80 characters) | when (payload.keywords = trigger keywords to add). reason: one line <=120 characters. At most 5 per day; a key + action dismissed within 30 days is refused; the same key + action still pending is returned as is.",
	}, memorySuggestHandler(b))
}

func memoryFindingsHandler(b Backend) mcp.ToolHandlerFor[memoryFindingsInput, memoryFindingsOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in memoryFindingsInput) (*mcp.CallToolResult, memoryFindingsOutput, error) {
		items, err := b.MemoryFindings(strings.TrimSpace(in.TrackerID), in.All)
		if err != nil {
			return nil, memoryFindingsOutput{}, err
		}
		if items == nil {
			items = []today.MemoryFinding{}
		}
		return nil, memoryFindingsOutput{Findings: items, DailyCap: today.MemorySuggestDailyCap}, nil
	}
}

func memorySuggestHandler(b Backend) mcp.ToolHandlerFor[today.MemorySuggestInput, memorySuggestOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in today.MemorySuggestInput) (*mcp.CallToolResult, memorySuggestOutput, error) {
		if strings.TrimSpace(in.TrackerID) == "" || strings.TrimSpace(in.Key) == "" || strings.TrimSpace(in.Action) == "" {
			return nil, memorySuggestOutput{}, fmt.Errorf("tracker_id, key and action are required")
		}
		sg, recorded, err := b.SuggestMemory(in)
		if err != nil {
			return nil, memorySuggestOutput{}, err
		}
		note := "recorded as a 「今天」 card: the person adopts or dismisses it; nothing was changed"
		if !recorded {
			note = "the same key + action is already waiting for the person"
		}
		return nil, memorySuggestOutput{Suggestion: sg, Recorded: recorded, Note: note}, nil
	}
}

func (b *clientBackend) MemoryFindings(trackerID string, all bool) ([]today.MemoryFinding, error) {
	return b.cli.MemoryFindings(trackerID, all)
}

func (b *clientBackend) SuggestMemory(in today.MemorySuggestInput) (today.MemorySuggestion, bool, error) {
	return b.cli.SuggestMemory(in)
}

func (b *localBackend) MemoryFindings(string, bool) ([]today.MemoryFinding, error) {
	return nil, errNeedsServer
}

func (b *localBackend) SuggestMemory(today.MemorySuggestInput) (today.MemorySuggestion, bool, error) {
	return today.MemorySuggestion{}, false, errNeedsServer
}
