package sources

import (
	"context"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
)

// PreviousReader is the part of *jobstore.Store the previous-synthesis adapter reads.
type PreviousReader interface {
	LatestSucceededMemorySynthesis(project, day string) (jobstore.MemorySynthesisRun, bool, error)
}

// Previous contributes the most recent succeeded synthesis BEFORE the day being built, so
// consecutive days can refer to each other without a run ever consuming its own output.
type Previous struct {
	Config
	Store PreviousReader
	// MaxRunes caps the carried result (default 1500).
	MaxRunes int
}

func (s Previous) Collect(_ context.Context, project, day string) ([]memorysynthesis.Item, error) {
	w, err := s.window(day)
	if err != nil {
		return nil, err
	}
	r, ok, err := s.Store.LatestSucceededMemorySynthesis(project, day)
	if err != nil || !ok || r.ProjectKey != project {
		return nil, err
	}
	limit := s.MaxRunes
	if limit <= 0 {
		limit = 1500
	}
	text := []rune(r.ResultJSON)
	if len(text) > limit {
		text = text[:limit]
	}
	return []memorysynthesis.Item{{
		Kind: KindPrevious, OccurredAt: stamp(w, r.FinishedAt), Title: "synthesis " + r.Day,
		SourceRef: "synthesis:" + r.ID, Text: string(text),
	}}, nil
}
