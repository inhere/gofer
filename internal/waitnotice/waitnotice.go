// Package waitnotice tells a person, once, that something entered the 「等我」 list, and
// reminds them once more if it stays there. It is the only sender of the 「等我」 notices: the
// desktop, webhook and Web Push channels all hang off Sender, so they cannot disagree.
//
// The scan compares the current list with the waiting_notices rows and stamps the row where it
// sends, so a restart repeats nothing and skips nothing. A notice carries a title and a
// category, never the text of a turn.
package waitnotice

import (
	"fmt"
	"sync"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Subject is one entry of the 「等我」 list that a person is told about.
type Subject struct {
	// Key is the subject key ("session:<id>", "job:<id>"); it is also the Web Push tag.
	Key string
	// Category is the 「等我」 category id (answer, approval ...).
	Category string
	// CallerID is the owner the notice goes to ("" = every caller that sees the project).
	CallerID   string
	ProjectKey string
	// Title is what the person knows the subject by: the seat name, the session title or the
	// job title.
	Title string
	// ThreadID is the workbench thread the notice opens ("r:<sid>" for a terminal session).
	ThreadID string
}

// Notice is one thing to send. Reminder marks the second and last notice of a stay.
type Notice struct {
	Subject
	Reminder bool
}

// Source answers what waits for a person right now.
type Source interface {
	Waiting() ([]Subject, error)
}

// Sender delivers one notice on every channel. It must not block.
type Sender interface {
	Send(Notice)
}

// Store is the part of the job store the scan keeps its record in.
type Store interface {
	ListOpenWaitingNotices() ([]jobstore.WaitingNotice, error)
	EnterWaitingNotice(caller, subjectKey, category string, at int64) (jobstore.WaitingNotice, bool, error)
	MarkWaitingNotified(id, at int64) error
	MarkWaitingReminded(id, at int64) error
	ClearWaitingNotice(id, at int64) error
}

// Scanner runs the comparison. ReminderAfter is read on every pass (0 = no reminder) so a
// config reload applies to the next one.
type Scanner struct {
	Store         Store
	Source        Source
	Sender        Sender
	ReminderAfter func() time.Duration
	Now           func() time.Time

	mu sync.Mutex
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Sweep makes one pass. A failing source ends the pass before anything is written: a list
// that could not be read says nothing about who left it.
func (s *Scanner) Sweep() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subjects, err := s.Source.Waiting()
	if err != nil {
		return fmt.Errorf("waitnotice: read waiting list: %w", err)
	}
	open, err := s.Store.ListOpenWaitingNotices()
	if err != nil {
		return fmt.Errorf("waitnotice: read notices: %w", err)
	}
	rows := make(map[string]jobstore.WaitingNotice, len(open))
	for _, n := range open {
		rows[n.SubjectKey] = n
	}
	at := s.now().Unix()
	var remindAfter time.Duration
	if s.ReminderAfter != nil {
		remindAfter = s.ReminderAfter()
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	seen := make(map[string]struct{}, len(subjects))
	for _, sub := range subjects {
		if _, dup := seen[sub.Key]; dup {
			continue
		}
		seen[sub.Key] = struct{}{}
		row, ok := rows[sub.Key]
		if !ok {
			var err error
			if row, _, err = s.Store.EnterWaitingNotice(sub.CallerID, sub.Key, sub.Category, at); err != nil {
				note(err)
				continue
			}
		}
		switch {
		case row.NotifiedAt == 0:
			s.Sender.Send(Notice{Subject: sub})
			note(s.Store.MarkWaitingNotified(row.ID, at))
		case remindAfter > 0 && row.RemindedAt == 0 && at-row.NotifiedAt >= int64(remindAfter/time.Second):
			s.Sender.Send(Notice{Subject: sub, Reminder: true})
			note(s.Store.MarkWaitingReminded(row.ID, at))
		}
	}
	for key, row := range rows {
		if _, still := seen[key]; !still {
			note(s.Store.ClearWaitingNotice(row.ID, at))
		}
	}
	return firstErr
}
