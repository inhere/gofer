package today

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// N3 T4 管家建议 (design §5): the steward advises on a card — one line of text, optionally
// the action it would take (the console turns it into the leading 「按建议：X」 button, which
// a PERSON clicks) and, mostly for review cards, a 3–5 line digest shown in 「详情」. Nothing
// here executes an action: the steward only writes advice.

// Advice limits.
const (
	AdviceTextRunes   = 60
	AdviceDigestLines = 5
	adviceDigestRunes = 600
)

var (
	// ErrInvalidAdvice is a malformed advice (text / digest / action).
	ErrInvalidAdvice = errors.New("today: invalid advice")
	// ErrUnknownCard is advice on a card that is not in the current queue.
	ErrUnknownCard = errors.New("today: no such card in the current queue")
)

// AdviceInput is POST /v1/today/advice and gofer_today_advise.
type AdviceInput struct {
	CardKey  string `json:"card_key"`
	Text     string `json:"text"`
	ActionID string `json:"action_id,omitempty"`
	Digest   string `json:"digest,omitempty"`
}

// ActionKey is the stable id of a card action: answer actions are told apart by value
// (answer:<value>), the rest by id. Advice action_id uses it.
func ActionKey(a Action) string {
	if a.ID == "answer" {
		return "answer:" + a.Value
	}
	return a.ID
}

// Advisable reports whether an action of a card of this kind can be 「按建议」: a one-click
// decision — not a reply box, not a navigation link, and never an option of a decision
// card (the steward only adds background there; design §5).
func Advisable(kind string, a Action) bool {
	return kind != KindDecision && !a.NeedsText && a.ID != "diff" && a.ID != "open"
}

// Advise validates and stores the advice for one card of the current queue. by is the
// speaker label (steward(<agent>) or human:<id>), jobID the steward's job when it wrote it.
func (s *Service) Advise(in AdviceInput, by, jobID string) (Advice, error) {
	in.CardKey, in.ActionID = strings.TrimSpace(in.CardKey), strings.TrimSpace(in.ActionID)
	in.Text = strings.Join(strings.Fields(in.Text), " ")
	digest, err := normalizeDigest(in.Digest)
	if err != nil {
		return Advice{}, err
	}
	switch {
	case in.CardKey == "":
		return Advice{}, fmt.Errorf("%w: card_key is required", ErrInvalidAdvice)
	case in.Text == "":
		return Advice{}, fmt.Errorf("%w: text is required (one line, at most %d characters)", ErrInvalidAdvice, AdviceTextRunes)
	case utf8.RuneCountInString(in.Text) > AdviceTextRunes:
		return Advice{}, fmt.Errorf("%w: text has %d characters, at most %d", ErrInvalidAdvice, utf8.RuneCountInString(in.Text), AdviceTextRunes)
	}
	cards, err := s.Decisions(true)
	if err != nil {
		return Advice{}, err
	}
	var card Card
	ok := false
	for _, c := range cards {
		if c.Key == in.CardKey {
			card, ok = c, true
			break
		}
	}
	if !ok {
		return Advice{}, fmt.Errorf("%w: %s (it may have been handled already)", ErrUnknownCard, in.CardKey)
	}
	if in.ActionID != "" {
		if card.Kind == KindDecision {
			return Advice{}, fmt.Errorf("%w: a decision card only gets background, never a picked option (leave action_id empty)", ErrInvalidAdvice)
		}
		var valid []string
		matched := false
		for _, a := range card.Actions {
			if !Advisable(card.Kind, a) {
				continue
			}
			k := ActionKey(a)
			valid = append(valid, k)
			if k == in.ActionID {
				matched = true
			}
		}
		if !matched {
			return Advice{}, fmt.Errorf("%w: action_id %q is not one of this card's actions (%s)", ErrInvalidAdvice, in.ActionID, strings.Join(valid, ", "))
		}
	}
	row, err := s.d.Store.UpsertDecisionAdvice(jobstore.DecisionAdvice{CardKey: in.CardKey, Text: in.Text, ActionID: in.ActionID,
		Digest: digest, By: by, At: s.d.Now().Unix(), JobID: jobID})
	if err != nil {
		return Advice{}, err
	}
	raw, _ := json.Marshal(AdviceInput{CardKey: in.CardKey, Text: in.Text, ActionID: in.ActionID, Digest: digest})
	if _, err := s.d.Store.AppendAuditEvent(jobstore.TodayAdviceAudit, in.CardKey, by, string(raw)); err != nil {
		return Advice{}, err
	}
	return adviceOf(row), nil
}

// normalizeDigest trims the digest to its non-empty lines and checks the limits.
func normalizeDigest(raw string) (string, error) {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > AdviceDigestLines {
		return "", fmt.Errorf("%w: digest has %d lines, at most %d", ErrInvalidAdvice, len(lines), AdviceDigestLines)
	}
	out := strings.Join(lines, "\n")
	if utf8.RuneCountInString(out) > adviceDigestRunes {
		return "", fmt.Errorf("%w: digest is longer than %d characters", ErrInvalidAdvice, adviceDigestRunes)
	}
	return out, nil
}

func adviceOf(a jobstore.DecisionAdvice) Advice {
	return Advice{Text: a.Text, ActionID: a.ActionID, Digest: a.Digest, By: a.By, At: a.At}
}

// CurrentAdvice reads one card's stored advice (nil when there is none).
func (s *Service) CurrentAdvice(cardKey string) (*Advice, error) {
	a, ok, err := s.d.Store.GetDecisionAdvice(strings.TrimSpace(cardKey))
	if err != nil || !ok {
		return nil, err
	}
	out := adviceOf(a)
	return &out, nil
}

// applyAdvice fills each card's advice (a review card's digest also becomes its
// review.digest) and prunes the advice whose card is gone. A row whose card is merely not
// in this list (an exec review hidden by the toggle, a snoozed card) is kept: liveness is
// checked against the card's own source.
func (s *Service) applyAdvice(cards []Card) error {
	rows, err := s.d.Store.ListDecisionAdvice()
	if err != nil || len(rows) == 0 {
		return err
	}
	idx := make(map[string]int, len(cards))
	for i := range cards {
		idx[cards[i].Key] = i
	}
	var dead []string
	for _, a := range rows {
		if i, ok := idx[a.CardKey]; ok {
			adv := adviceOf(a)
			cards[i].Advice = &adv
			if cards[i].Review != nil && a.Digest != "" {
				cards[i].Review.Digest = a.Digest
			}
			continue
		}
		alive, err := s.cardAlive(a.CardKey)
		if err != nil {
			return err
		}
		if !alive {
			dead = append(dead, a.CardKey)
		}
	}
	return s.d.Store.DeleteDecisionAdvice(dead...)
}

// PruneAdvice drops the advice of every card that is gone; it returns how many it removed.
func (s *Service) PruneAdvice() (int, error) {
	rows, err := s.d.Store.ListDecisionAdvice()
	if err != nil {
		return 0, err
	}
	var dead []string
	for _, a := range rows {
		alive, err := s.cardAlive(a.CardKey)
		if err != nil {
			return 0, err
		}
		if !alive {
			dead = append(dead, a.CardKey)
		}
	}
	return len(dead), s.d.Store.DeleteDecisionAdvice(dead...)
}

// cardAlive reports whether the source behind a card key still asks for a person — the
// same inclusion rules as the queue builder, read per card.
func (s *Service) cardAlive(key string) (bool, error) {
	st := s.d.Store
	kind, ref, _ := strings.Cut(key, ":")
	switch kind {
	case KindInteraction:
		jobID, iid, _ := strings.Cut(ref, "/")
		rows, err := st.ListInteractions(jobID)
		if err != nil {
			return false, err
		}
		for _, it := range rows {
			if it.ID == iid {
				return it.Status == job.InteractionPending, nil
			}
		}
		return false, nil
	case KindDecision:
		d, ok, err := st.GetDecision(ref)
		return ok && d.State == jobstore.DecisionOpen, err
	case KindRelay:
		open, err := st.ListDecisions(jobstore.DecisionOpen, "")
		if err != nil {
			return false, err
		}
		for _, d := range open {
			if d.Kind == jobstore.DecisionKindRelay && d.SessionID == ref && d.AckedAt == 0 {
				return true, nil
			}
		}
		return false, nil
	case KindReview:
		rec, ok, err := st.GetJob(ref)
		return ok && rec.Status == job.StatusNeedsReview, err
	case KindWork:
		w, ok, err := st.GetWorkItem(ref)
		if err != nil || !ok || jobstore.WorkStatusFinal(w.Status) || w.MergedInto != "" {
			return false, err
		}
		now := s.d.Now().Unix()
		return w.Status == jobstore.WorkNeedsMe || w.Status == jobstore.WorkNeedsOnsite ||
			(w.RemindAt > 0 && w.RemindAt <= now) ||
			(w.Status == jobstore.WorkParked && w.ParkUntil > 0 && w.ParkUntil <= now), nil
	case KindSuggestion:
		wid, field, _ := strings.Cut(ref, ":")
		sg, ok, err := st.GetWorkSuggestion(wid, field)
		return ok && sg.State == jobstore.SuggestionPending, err
	case KindMerge:
		id, err := strconv.ParseInt(ref, 10, 64)
		if err != nil {
			return false, nil
		}
		g, err := st.GetWorkMergeSuggestion(id)
		if errors.Is(err, jobstore.ErrMergeSuggestionNotFound) {
			return false, nil
		}
		return err == nil && g.State == jobstore.MergeSuggestPending, err
	case KindPlanBlocked:
		p, ok, err := st.GetPlan(ref)
		return ok && p.Status == jobstore.PlanBlocked, err
	}
	return false, nil
}

// adviceKinds are the card kinds the steward is asked to advise on (design §5; decision
// cards get background only).
var adviceKinds = map[string]bool{KindReview: true, KindSuggestion: true, KindMerge: true, KindInteraction: true, KindDecision: true}

// UnadvisedCount counts the queue's cards of the advised kinds that have no advice yet:
// the steward's review runs (and gets the advice step) while it is positive.
func (s *Service) UnadvisedCount() (int, error) {
	cards, err := s.Decisions(false)
	if err != nil {
		return 0, err
	}
	if err := s.applyAdvice(cards); err != nil {
		return 0, err
	}
	n := 0
	for _, c := range cards {
		if c.Advice == nil && adviceKinds[c.Kind] {
			n++
		}
	}
	return n, nil
}
