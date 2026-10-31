package sources

import (
	"context"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
)

// WorkReader is the part of *jobstore.Store the work adapter reads.
type WorkReader interface {
	ListWorkItems(jobstore.WorkListOpts) ([]jobstore.WorkItem, error)
	ListWorkJournalLevel(id string, limit int, before int64, level string) ([]jobstore.WorkJournalEntry, error)
}

// Work collects work items active inside the window and their milestone journal lines.
type Work struct {
	Config
	Store WorkReader
	// MaxItems / MaxJournal bound the item count and the journal lines read per item.
	MaxItems   int
	MaxJournal int
}

func (s Work) Collect(_ context.Context, project, day string) ([]memorysynthesis.Item, error) {
	w, err := s.window(day)
	if err != nil {
		return nil, err
	}
	maxItems, maxJournal := s.MaxItems, s.MaxJournal
	if maxItems <= 0 {
		maxItems = 200
	}
	if maxJournal <= 0 {
		maxJournal = 20
	}
	rows, err := s.Store.ListWorkItems(jobstore.WorkListOpts{Project: project, IncludeClosed: true, Limit: maxItems})
	if err != nil {
		return nil, err
	}
	var out []memorysynthesis.Item
	for _, it := range rows {
		// The store filter is advisory; the adapter re-checks visibility itself.
		if it.ProjectKey != project || it.MergedInto != "" {
			continue
		}
		var journal []jobstore.WorkJournalEntry
		entries, err := s.Store.ListWorkJournalLevel(it.ID, maxJournal, 0, jobstore.WorkLevelMilestone)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if w.Contains(e.At) {
				journal = append(journal, e)
			}
		}
		if !w.Contains(it.LastActivityAt) && !w.Contains(it.UpdatedAt) && len(journal) == 0 {
			continue
		}
		out = append(out, memorysynthesis.Item{
			Kind: KindWorkItem, OccurredAt: stamp(w, it.LastActivityAt), Title: it.Title, SourceRef: "work:" + it.ID,
			Text: kv("status", it.Status, "goal", it.Goal, "next_step", it.NextStep, "blocker", it.BlockerText, "summary", it.Summary),
		})
		for _, e := range journal {
			out = append(out, memorysynthesis.Item{
				Kind: KindWorkJournal, OccurredAt: stamp(w, e.At), Title: it.Title, Text: e.Text,
				SourceRef: "work:" + it.ID + "#journal-" + itoa(e.ID),
			})
		}
	}
	return out, nil
}
