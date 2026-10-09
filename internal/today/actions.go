package today

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// ErrInvalidAction is a malformed POST /v1/today/actions body.
var ErrInvalidAction = errors.New("today: invalid action")

// MaxHandledDays caps GET /v1/today/handled?days=.
const MaxHandledDays = 30

// ActionInput is what the console records after a card action succeeded (design
// §2.3): the card, the action it took, the steward's advice action at that moment (T4),
// and the display bits the 「已处理」 drawer shows once the card is gone.
type ActionInput struct {
	CardKey        string `json:"card_key"`
	ActionID       string `json:"action_id"`
	AdviceActionID string `json:"advice_action_id,omitempty"`
	Title          string `json:"title,omitempty"`
	Label          string `json:"label,omitempty"`
	Kind           string `json:"kind,omitempty"`
}

// Handled is one 「已处理」 row.
type Handled struct {
	At             int64  `json:"at"`
	Actor          string `json:"actor,omitempty"`
	CardKey        string `json:"card_key"`
	Kind           string `json:"kind,omitempty"`
	Title          string `json:"title,omitempty"`
	ActionID       string `json:"action_id"`
	Label          string `json:"label,omitempty"`
	AdviceActionID string `json:"advice_action_id,omitempty"`
}

// RecordAction appends one today.action audit row.
func (s *Service) RecordAction(in ActionInput, actor string) (Handled, error) {
	in.CardKey, in.ActionID = strings.TrimSpace(in.CardKey), strings.TrimSpace(in.ActionID)
	if in.CardKey == "" || in.ActionID == "" {
		return Handled{}, fmt.Errorf("%w: card_key and action_id are required", ErrInvalidAction)
	}
	if in.Kind == "" {
		in.Kind, _, _ = strings.Cut(in.CardKey, ":")
	}
	in.Title, in.Label = capRunes(strings.TrimSpace(in.Title), 200), capRunes(strings.TrimSpace(in.Label), 60)
	raw, err := json.Marshal(in)
	if err != nil {
		return Handled{}, err
	}
	if _, err := s.d.Store.AppendAuditEvent(jobstore.TodayActionAudit, in.CardKey, actor, string(raw)); err != nil {
		return Handled{}, err
	}
	return Handled{At: s.d.Now().Unix(), Actor: actor, CardKey: in.CardKey, Kind: in.Kind, Title: in.Title,
		ActionID: in.ActionID, Label: in.Label, AdviceActionID: in.AdviceActionID}, nil
}

// HandledSince lists the today.action rows of the last `days` days, newest first.
func (s *Service) HandledSince(days int) ([]Handled, error) {
	if days <= 0 {
		days = 7
	}
	if days > MaxHandledDays {
		days = MaxHandledDays
	}
	since := s.d.Now().Unix() - int64(days)*86400
	rows, err := s.d.Store.ListAuditEventsSince(jobstore.TodayActionAudit, since, 500)
	if err != nil {
		return nil, err
	}
	out := make([]Handled, 0, len(rows))
	for _, r := range rows {
		var in ActionInput
		_ = json.Unmarshal([]byte(r.Detail), &in)
		out = append(out, Handled{At: r.At, Actor: r.Actor, CardKey: r.TargetID, Kind: in.Kind, Title: in.Title,
			ActionID: in.ActionID, Label: in.Label, AdviceActionID: in.AdviceActionID})
	}
	return out, nil
}
