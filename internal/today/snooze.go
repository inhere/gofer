package today

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// N3 T3 「稍后」 (design §2.3): a snoozed card leaves the queue until its time passes, the
// job it waits on ends, or the card itself moves (its activity_at becomes newer than the
// one recorded at snooze time) — then it comes back flagged Woke. A card whose deadline
// (expires_at) is near is NOT pulled back: the source's own timeout still applies.

// Wake reasons (Card.WokeReason).
const (
	WokeTime     = "time"
	WokeJob      = "job"
	WokeActivity = "activity"
)

// SnoozeActionID is the today.action audit id a snooze records.
const SnoozeActionID = "snooze"

const (
	// MaxSnoozeSec caps until_at (30 days ahead).
	MaxSnoozeSec = 30 * 86400
	// wokeMarkerSec is how long a woken card keeps its flag when it is not handled.
	wokeMarkerSec = 86400
)

var (
	// ErrInvalidSnooze is a malformed snooze request.
	ErrInvalidSnooze = errors.New("today: invalid snooze")
	// ErrCardNotQueued is a snooze for a card that is not in the queue (any more).
	ErrCardNotQueued = errors.New("today: card not in the queue")
)

// SnoozeInput is the POST /v1/today/snooze body: exactly one of UntilAt / UntilJobID.
type SnoozeInput struct {
	CardKey    string `json:"card_key"`
	UntilAt    int64  `json:"until_at,omitempty"`
	UntilJobID string `json:"until_job_id,omitempty"`
}

// SnoozedCard is one 「已稍后」 row.
type SnoozedCard struct {
	CardKey    string `json:"card_key"`
	Kind       string `json:"kind"`
	Tag        string `json:"tag,omitempty"`
	Title      string `json:"title"`
	ProjectKey string `json:"project_key,omitempty"`
	UntilAt    int64  `json:"until_at,omitempty"`
	UntilJobID string `json:"until_job_id,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

func snoozedOf(r jobstore.TodaySnooze, c Card) SnoozedCard {
	return SnoozedCard{CardKey: r.CardKey, Kind: c.Kind, Tag: c.Tag, Title: c.Title, ProjectKey: c.ProjectKey,
		UntilAt: r.UntilAt, UntilJobID: r.UntilJobID, ExpiresAt: c.ExpiresAt, CreatedAt: r.CreatedAt}
}

// Snooze takes a card out of the queue and records the today.action audit.
func (s *Service) Snooze(in SnoozeInput, actor string) (SnoozedCard, error) {
	in.CardKey, in.UntilJobID = strings.TrimSpace(in.CardKey), strings.TrimSpace(in.UntilJobID)
	if in.CardKey == "" {
		return SnoozedCard{}, fmt.Errorf("%w: card_key is required", ErrInvalidSnooze)
	}
	if (in.UntilAt > 0) == (in.UntilJobID != "") {
		return SnoozedCard{}, fmt.Errorf("%w: give exactly one of until_at / until_job_id", ErrInvalidSnooze)
	}
	now := s.d.Now()
	label := ""
	if in.UntilAt > 0 {
		if in.UntilAt <= now.Unix() || in.UntilAt > now.Unix()+MaxSnoozeSec {
			return SnoozedCard{}, fmt.Errorf("%w: until_at must be in the next 30 days", ErrInvalidSnooze)
		}
		label = "稍后到 " + time.Unix(in.UntilAt, 0).In(now.Location()).Format("01-02 15:04")
	} else {
		rec, ok, err := s.d.Store.GetJob(in.UntilJobID)
		if err != nil {
			return SnoozedCard{}, err
		}
		if !ok {
			return SnoozedCard{}, fmt.Errorf("%w: job %s not found", ErrInvalidSnooze, in.UntilJobID)
		}
		if job.IsTerminal(rec.Status) {
			return SnoozedCard{}, fmt.Errorf("%w: job %s already ended", ErrInvalidSnooze, in.UntilJobID)
		}
		label = "等 job " + in.UntilJobID + " 结束"
	}
	cards, err := s.Decisions(true)
	if err != nil {
		return SnoozedCard{}, err
	}
	card, ok := cardByKey(cards, in.CardKey)
	if !ok {
		return SnoozedCard{}, fmt.Errorf("%w: %s", ErrCardNotQueued, in.CardKey)
	}
	row, err := s.d.Store.UpsertTodaySnooze(jobstore.TodaySnooze{CardKey: card.Key, UntilAt: in.UntilAt,
		UntilJobID: in.UntilJobID, ActivityAt: card.ActivityAt})
	if err != nil {
		return SnoozedCard{}, err
	}
	if _, err := s.RecordAction(ActionInput{CardKey: card.Key, ActionID: SnoozeActionID, Title: card.Title,
		Label: label, Kind: card.Kind}, actor); err != nil {
		return SnoozedCard{}, err
	}
	return snoozedOf(row, card), nil
}

// Unsnooze puts a card back in the queue; ok is false when it was not snoozed.
func (s *Service) Unsnooze(cardKey string) (bool, error) {
	cardKey = strings.TrimSpace(cardKey)
	if cardKey == "" {
		return false, fmt.Errorf("%w: card_key is required", ErrInvalidSnooze)
	}
	n, err := s.d.Store.DeleteTodaySnoozes(cardKey)
	return n > 0, err
}

// Snoozed lists the cards that are snoozed right now (wake rules applied first).
func (s *Service) Snoozed(includeExec bool) ([]SnoozedCard, error) {
	cards, err := s.Decisions(includeExec)
	if err != nil {
		return nil, err
	}
	_, out, err := s.applySnoozes(cards, includeExec)
	return out, err
}

func cardByKey(cards []Card, key string) (Card, bool) {
	for _, c := range cards {
		if c.Key == key {
			return c, true
		}
	}
	return Card{}, false
}

// applySnoozes drops the actively snoozed cards from the queue, wakes the snoozes whose
// rule fired (flagging the card), and cleans the rows whose card is gone.
func (s *Service) applySnoozes(cards []Card, includeExec bool) ([]Card, []SnoozedCard, error) {
	rows, err := s.d.Store.ListTodaySnoozes()
	if err != nil || len(rows) == 0 {
		return cards, []SnoozedCard{}, err
	}
	now := s.d.Now().Unix()
	idx := make(map[string]int, len(cards))
	for i := range cards {
		idx[cards[i].Key] = i
	}
	hidden := map[string]bool{}
	snoozed := make([]SnoozedCard, 0, len(rows))
	var gone []string
	for _, r := range rows {
		i, ok := idx[r.CardKey]
		if !ok {
			keep, err := s.hiddenExecReview(r.CardKey, includeExec)
			if err != nil {
				return nil, nil, err
			}
			if !keep {
				gone = append(gone, r.CardKey)
			}
			continue
		}
		c := &cards[i]
		if r.WokeAt > 0 {
			if now-r.WokeAt > wokeMarkerSec {
				gone = append(gone, r.CardKey)
			} else {
				c.Woke, c.WokeReason = true, r.WokeReason
			}
			continue
		}
		reason, err := s.wakeReason(r, *c, now)
		if err != nil {
			return nil, nil, err
		}
		if reason != "" {
			if err := s.d.Store.WakeTodaySnooze(r.CardKey, reason); err != nil {
				return nil, nil, err
			}
			c.Woke, c.WokeReason = true, reason
			continue
		}
		hidden[r.CardKey] = true
		snoozed = append(snoozed, snoozedOf(r, *c))
	}
	if _, err := s.d.Store.DeleteTodaySnoozes(gone...); err != nil {
		return nil, nil, err
	}
	if len(hidden) == 0 {
		return cards, snoozed, nil
	}
	visible := make([]Card, 0, len(cards))
	for _, c := range cards {
		if !hidden[c.Key] {
			visible = append(visible, c)
		}
	}
	return visible, snoozed, nil
}

// wakeReason says which rule ends an active snooze ("" = still snoozed). New activity
// wins over the other two so the flag says 「有新动静」 when that is what happened.
func (s *Service) wakeReason(r jobstore.TodaySnooze, c Card, now int64) (string, error) {
	if c.ActivityAt > r.ActivityAt {
		return WokeActivity, nil
	}
	if r.UntilAt > 0 && now >= r.UntilAt {
		return WokeTime, nil
	}
	if r.UntilJobID != "" {
		rec, ok, err := s.d.Store.GetJob(r.UntilJobID)
		if err != nil {
			return "", err
		}
		if !ok || job.IsTerminal(rec.Status) {
			return WokeJob, nil
		}
	}
	return "", nil
}

// hiddenExecReview keeps the row of a snoozed exec review card that is only missing
// because this read left exec jobs out (the job still waits for review).
func (s *Service) hiddenExecReview(key string, includeExec bool) (bool, error) {
	jobID, ok := strings.CutPrefix(key, KindReview+":")
	if includeExec || !ok {
		return false, nil
	}
	rec, found, err := s.d.Store.GetJob(jobID)
	if err != nil {
		return false, err
	}
	return found && rec.Status == job.StatusNeedsReview, nil
}
