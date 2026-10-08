package work

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// DeleteFailure is one item a delete sweep could not remove.
type DeleteFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// DeleteResult is the outcome of Delete: what went, and what stayed with why.
type DeleteResult struct {
	Deleted []string        `json:"deleted"`
	Failed  []DeleteFailure `json:"failed"`
}

// Delete permanently removes finished work items. ids names items explicitly; status
// (done | dropped, or empty) adds every item in that final status. Each item is its own
// transaction, so one that is still open (or already gone) is reported in Failed and does
// not stop the rest. actor is only recorded in the audit row (see Store.DeleteWorkItem).
func (s *Service) Delete(ids []string, status, actor string) (DeleteResult, error) {
	res := DeleteResult{Deleted: []string{}, Failed: []DeleteFailure{}}
	status = strings.TrimSpace(status)
	var all []string
	seen := map[string]bool{}
	add := func(id string) {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			all = append(all, id)
		}
	}
	for _, id := range ids {
		add(id)
	}
	if status != "" {
		byStatus, err := s.store.ListFinalWorkItemIDs(status)
		if err != nil {
			return res, err
		}
		for _, id := range byStatus {
			add(id)
		}
	}
	if len(all) == 0 && len(ids) == 0 && status == "" {
		return res, fmt.Errorf("%w: give ids and/or a status (done|dropped)", jobstore.ErrWorkInvalid)
	}
	for _, id := range all {
		if err := s.store.DeleteWorkItem(id, actor); err != nil {
			res.Failed = append(res.Failed, DeleteFailure{ID: id, Error: err.Error()})
			continue
		}
		res.Deleted = append(res.Deleted, id)
	}
	return res, nil
}
