package mail

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// The send limits (设计「默认数值」). Every value has a default here; the server config will
// override them when the mailbox's config keys land (M5), so nothing else in the package
// hard-codes a number. A zero field means "no limit" for that one.

// Limits are the mailbox's default numbers.
type Limits struct {
	// MaxBodyBytes is 单封正文 (32 KB): long enough for a log or a diff summary, short enough
	// that a letter cannot be a bulk payload.
	MaxBodyBytes int
	// PerMinute / PerHour are 发件限速 per sender (10 / 100). Exceeding them is
	// ErrRateLimited, which the entry layer answers with 429.
	PerMinute int
	PerHour   int
	// MaxThreadLetters is 对话串自动往返 (20): once a thread has that many letters, the next
	// one is parked in the capture box instead of delivered, so two agents answering each
	// other stop and a person takes over.
	MaxThreadLetters int
	// DeliverBy is 送达期限: how long a letter may stay undelivered before the sweep parks it
	// in the capture box.
	DeliverBy time.Duration
}

// DefaultLimits is the design's default number table, in code.
var DefaultLimits = Limits{
	MaxBodyBytes:     32 * 1024,
	PerMinute:        10,
	PerHour:          100,
	MaxThreadLetters: 20,
	DeliverBy:        jobstore.DefaultMailDeliverBy,
}

// rateWindows are the send-rate windows, longest last.
type rateWindow struct {
	seconds int64
	limit   int
	label   string
}

func (s *Service) rateWindows() []rateWindow {
	return []rateWindow{
		{seconds: 60, limit: s.Limits.PerMinute, label: "minute"},
		{seconds: 3600, limit: s.Limits.PerHour, label: "hour"},
	}
}

func (s *Service) deliverBy() int64 {
	if sec := s.deliverByOverride.Load(); sec > 0 {
		return s.now().Unix() + sec
	}
	if s.Limits.DeliverBy <= 0 {
		return 0 // the store then applies its own default deadline
	}
	return s.now().Unix() + int64(s.Limits.DeliverBy/time.Second)
}

// checkBody refuses an empty or oversized letter.
func (s *Service) checkBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return ErrInvalidBody
	}
	if max := s.Limits.MaxBodyBytes; max > 0 && len(body) > max {
		return fmt.Errorf("%w: %d bytes, at most %d", ErrMailTooLong, len(body), max)
	}
	return nil
}

// checkRate counts the sender's letters inside each window. Counting is the store's (it owns
// the rows); the ceilings are this package's.
func (s *Service) checkRate(org Origin) error {
	now := s.now().Unix()
	for _, w := range s.rateWindows() {
		if w.limit <= 0 {
			continue
		}
		n, err := s.store.CountMailFrom(org.Kind, org.ID, org.Caller, now-w.seconds)
		if err != nil {
			return err
		}
		if n >= w.limit {
			return fmt.Errorf("%w: %s already sent %d letters in the last %s (at most %d)",
				ErrRateLimited, org.Display(), n, w.label, w.limit)
		}
	}
	return nil
}

// IsRateLimited reports whether err is (or wraps) the send-rate sentinel — the entry layer's
// 429 decision without importing the wording.
func IsRateLimited(err error) bool { return errors.Is(err, ErrRateLimited) }
