package httpapi

import (
	"sync"

	"github.com/inhere/gofer/internal/inbox"
	"github.com/inhere/gofer/internal/pushhub"
)

// The `waiting` push topic: the 「等我」 list as a snapshot the console keeps in step with,
// so a page does not poll the whole of GET /v1/today to learn that something started (or
// stopped) waiting for it. The judgement itself is the one list everything else reads
// (waitingSubjectsFor → inbox.WaitingSubjects); this file only projects it onto the wire and
// decides when a change is worth a frame.
//
// A row carries what tells the person WHAT waits (key, class, kind, project, since when) and
// the card's title — never a summary or the text of a turn (the same rule the notices follow,
// waitnotice package).

// waitingView is one row of the `waiting` snapshot.
type waitingView struct {
	// Key is the subject key the list folds by ("job:<id>", "session:<id>", "plan:<id>" ...).
	Key string `json:"key"`
	// Category is the 「等我」 class id (approval, answer, review ...).
	Category string `json:"category"`
	// Kind is the card kind behind it (interaction, relay, acp_reply, review ...).
	Kind         string `json:"kind"`
	ProjectKey   string `json:"project_key,omitempty"`
	WaitingSince int64  `json:"waiting_since"`
	Title        string `json:"title,omitempty"`
}

// waitingListViews projects the subjects of the one judgement onto the wire shape, keeping the
// order of the list (the same order the inbox, the badge and the home page show).
func waitingListViews(subjects []inbox.Subject) []waitingView {
	out := make([]waitingView, 0, len(subjects))
	for _, sub := range subjects {
		out = append(out, waitingView{
			Key:          sub.Key,
			Category:     string(sub.Category),
			Kind:         sub.Card.Kind,
			ProjectKey:   sub.Card.ProjectKey,
			WaitingSince: sub.Card.WaitingSince,
			Title:        sub.Card.Title,
		})
	}
	return out
}

// waitingListSnapshot is the payload of the `waiting` topic for an unrestricted connection.
func (s *Server) waitingListSnapshot() (any, error) {
	subjects, err := s.waitingSubjectsFor("")
	if err != nil {
		return nil, err
	}
	return waitingListViews(subjects), nil
}

// waitingListSnapshotFor is the same payload for a connection under a caller ceiling (gofer-7798
// 第二期): a row is kept only when its project is inside the ceiling. A row without a project
// (the held-mail and server-health summaries) is dropped — fail closed, so a restricted caller
// never learns of work outside its range.
func (s *Server) waitingListSnapshotFor(scope func(string) bool) (any, error) {
	subjects, err := s.waitingSubjectsFor("")
	if err != nil {
		return nil, err
	}
	kept := make([]inbox.Subject, 0, len(subjects))
	for _, sub := range subjects {
		if p := sub.Card.ProjectKey; p != "" && scope(p) {
			kept = append(kept, sub)
		}
	}
	return waitingListViews(kept), nil
}

// waitingPushState remembers the set the last scan judged, so the sweep only tells the hub when
// that set really changed (a key appeared or left, or a subject moved to another class). Without
// it every 30s pass would recompute and re-push an identical snapshot.
type waitingPushState struct {
	mu   sync.Mutex
	last map[string]string // subject key -> category id
}

// noteWaitingSubjects records the set one scan just judged and, when it differs from the
// previous one, notifies the `waiting` topic. It is called from the scan itself (the notice
// source), so one judgement per pass serves both the notices and the push — no second read of
// the waiting list and no timer of its own.
func (s *Server) noteWaitingSubjects(subjects []inbox.Subject) {
	next := make(map[string]string, len(subjects))
	for _, sub := range subjects {
		next[sub.Key] = string(sub.Category)
	}
	s.waitingPush.mu.Lock()
	changed := len(next) != len(s.waitingPush.last)
	if !changed {
		for key, cat := range next {
			if s.waitingPush.last[key] != cat {
				changed = true
				break
			}
		}
	}
	s.waitingPush.last = next
	s.waitingPush.mu.Unlock()
	if changed && s.live != nil {
		s.live.Notify(pushhub.TopicWaiting)
	}
}
