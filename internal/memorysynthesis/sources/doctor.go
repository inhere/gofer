package sources

import (
	"context"
	"strings"

	"github.com/inhere/gofer/internal/memorysynthesis"
	"github.com/inhere/gofer/internal/today"
)

// DoctorReader is the part of *today.Service the memory doctor adapter reads.
type DoctorReader interface {
	MemoryFindings(today.MemoryFindingsQuery) ([]today.MemoryFinding, error)
	ListMemorySuggestions(state string) ([]today.MemorySuggestion, error)
}

// Doctor collects the memory doctor's findings (slug, key, kind, summary, open actions) and
// the pending maintenance suggestions of the project. It never forwards a memory body:
// today.MemoryFinding.Content is deliberately ignored.
type Doctor struct {
	Config
	Reader DoctorReader
}

func (s Doctor) Collect(_ context.Context, project, day string) ([]memorysynthesis.Item, error) {
	w, err := s.window(day)
	if err != nil {
		return nil, err
	}
	findings, err := s.Reader.MemoryFindings(today.MemoryFindingsQuery{All: true})
	if err != nil {
		return nil, err
	}
	var out []memorysynthesis.Item
	for _, f := range findings {
		if f.ProjectKey != project {
			continue
		}
		slugs := make([]string, 0, len(f.Findings))
		for _, fd := range f.Findings {
			slugs = append(slugs, strings.TrimSpace(fd.Slug+" "+fd.Detail))
		}
		out = append(out, memorysynthesis.Item{
			Kind: KindMemoryFind, Title: f.Key, SourceRef: "memory:" + f.TrackerID + "/" + f.Key,
			Text: kv("kind", f.Kind, "summary", f.Summary, "findings", strings.Join(slugs, "; "), "open_actions", strings.Join(f.Actions, ",")),
		})
	}
	sugs, err := s.Reader.ListMemorySuggestions("")
	if err != nil {
		return nil, err
	}
	for _, sg := range sugs {
		if sg.ProjectKey != project {
			continue
		}
		out = append(out, memorysynthesis.Item{
			Kind: KindMemorySugg, OccurredAt: stamp(w, sg.CreatedAt), Title: sg.Key,
			SourceRef: "memory-suggestion:" + itoa(sg.ID),
			Text:      kv("action", sg.Action, "state", sg.State, "text", sg.Text, "reason", sg.Reason),
		})
	}
	return out, nil
}
