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
}

// Doctor collects the memory doctor's findings (slug, key, kind, summary) of the project. It never forwards a memory body:
// today.MemoryFinding.Content is deliberately ignored.
type Doctor struct {
	Config
	Reader DoctorReader
}

func (s Doctor) Collect(_ context.Context, project, day string) ([]memorysynthesis.Item, error) {
	if _, err := s.window(day); err != nil {
		return nil, err
	}
	findings, err := s.Reader.MemoryFindings(today.MemoryFindingsQuery{})
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
			Text: kv("kind", f.Kind, "summary", f.Summary, "findings", strings.Join(slugs, "; ")),
		})
	}
	return out, nil
}
