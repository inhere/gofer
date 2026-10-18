package mail

import (
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// HoldLetter parks a letter that cannot be delivered (a pty job, a delivery deadline that
// passed) in the capture box: the letter becomes held with reason, and the box gets the item
// that points back at it (the same shape a send-time hold has). ok=false means the letter
// had already moved on (delivered, cancelled …) and nothing was recorded.
func (s *Service) HoldLetter(id int64, reason string) (jobstore.Mail, bool, error) {
	held, ok, err := s.store.HoldMail(id, reason)
	if err != nil || !ok {
		return held, false, err
	}
	return held, true, s.recordHeld(held)
}

// RecordHeld records letters the sweep already moved to held (ExpireOverdue) in the capture
// box. It keeps going after a failure so one broken item does not strand the others, and
// returns the first error.
func (s *Service) RecordHeld(held []jobstore.Mail) error {
	var first error
	for _, m := range held {
		if err := s.recordHeld(m); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Service) recordHeld(m jobstore.Mail) error {
	if m.CaptureItemID != "" {
		return nil
	}
	itemID, err := s.recordCapture(m, nil)
	if err != nil || itemID == "" {
		return err
	}
	_, err = s.store.SetMailCaptureItem(m.ID, itemID)
	return err
}

// Store exposes the metadata store the mailbox works over (the deliverer reads letters with
// it; there is one store per process).
func (s *Service) Store() *jobstore.Store { return s.store }

// SetDeliverBy changes the delivery deadline new letters get (mail.deliver_by_sec, hot
// reload); d <= 0 returns to Limits.DeliverBy. Safe to call while letters are being sent.
func (s *Service) SetDeliverBy(d time.Duration) {
	s.deliverByOverride.Store(int64(d / time.Second))
}
