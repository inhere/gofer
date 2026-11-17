package job

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InteractionExt is the optional extension of a choice interaction that rides inside the
// persisted options column (no schema change): multi-select, free text outside the options
// and a recommended option. Without any of them the column stays the plain options array,
// exactly as older versions wrote it.
type InteractionExt struct {
	Multi       bool
	AllowOther  bool
	Recommended string
}

// interactionOptionsEnvelope is the object form of the options column, written only when an
// extension field is set.
type interactionOptionsEnvelope struct {
	Options     []InteractionOption `json:"options,omitempty"`
	Multi       bool                `json:"multi,omitempty"`
	AllowOther  bool                `json:"allow_other,omitempty"`
	Recommended string              `json:"recommended,omitempty"`
}

// encodeInteractionOptions marshals an interaction's options (and extension) for the
// options column; "" when there is nothing to store.
func encodeInteractionOptions(it Interaction) string {
	if it.Multi || it.AllowOther || it.Recommended != "" {
		b, err := json.Marshal(interactionOptionsEnvelope{
			Options: it.Options, Multi: it.Multi, AllowOther: it.AllowOther, Recommended: it.Recommended,
		})
		if err != nil {
			return ""
		}
		return string(b)
	}
	if len(it.Options) == 0 {
		return ""
	}
	b, err := json.Marshal(it.Options)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeInteractionOptions reads the options column in either form (plain array, or the
// envelope carrying the extension). An unparseable blob yields no options rather than an
// error, like every other reader of the column.
func DecodeInteractionOptions(raw string) ([]InteractionOption, InteractionExt) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, InteractionExt{}
	}
	if raw[0] == '[' {
		var opts []InteractionOption
		_ = json.Unmarshal([]byte(raw), &opts)
		return opts, InteractionExt{}
	}
	var env interactionOptionsEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, InteractionExt{}
	}
	return env.Options, InteractionExt{Multi: env.Multi, AllowOther: env.AllowOther, Recommended: env.Recommended}
}

// InteractionFromRecord is the exported projection of a persisted interaction row, for
// read paths outside this package that must see the same value the service returns.
var InteractionFromRecord = fromInteractionRecord

// validateInteractionAnswer checks an answer against the interaction it answers. Only a
// choice interaction that carries options is constrained: a single-select answer must be
// one of the options (value or label) or, with allow_other, non-empty free text; a
// multi-select answer is a JSON array of option labels (free text entries only with
// allow_other). Every other interaction type keeps accepting any answer.
func validateInteractionAnswer(it Interaction, answer string) error {
	if it.Type != InteractionTypeChoice || len(it.Options) == 0 {
		return nil
	}
	isOption := func(v string) bool {
		for _, o := range it.Options {
			if v == o.Value || (o.Label != "" && v == o.Label) {
				return true
			}
		}
		return false
	}
	if it.Multi {
		var picked []string
		if err := json.Unmarshal([]byte(answer), &picked); err != nil {
			return fmt.Errorf("%w: a multi-select answer must be a JSON array of option labels", ErrInvalidInteraction)
		}
		if len(picked) == 0 {
			return fmt.Errorf("%w: pick at least one option", ErrInvalidInteraction)
		}
		for _, p := range picked {
			if isOption(p) {
				continue
			}
			if !it.AllowOther || strings.TrimSpace(p) == "" {
				return fmt.Errorf("%w: %q is not one of the options", ErrInvalidInteraction, p)
			}
		}
		return nil
	}
	if isOption(answer) {
		return nil
	}
	if it.AllowOther && strings.TrimSpace(answer) != "" {
		return nil
	}
	return fmt.Errorf("%w: answer %q is not one of the options", ErrInvalidInteraction, answer)
}

// validateInteractionExt rejects an extension that does not fit its options: multi-select
// and a recommendation only make sense with options, and the recommendation must be one.
func validateInteractionExt(in InteractionInput) error {
	if !in.Multi && in.Recommended == "" {
		return nil
	}
	if len(in.Options) == 0 {
		return fmt.Errorf("%w: multi / recommended need options", ErrInvalidInteraction)
	}
	if in.Recommended == "" {
		return nil
	}
	for _, o := range in.Options {
		if in.Recommended == o.Value || (o.Label != "" && in.Recommended == o.Label) {
			return nil
		}
	}
	return fmt.Errorf("%w: recommended %q is not one of the options", ErrInvalidInteraction, in.Recommended)
}

// armInteractionExpiry cancels a non-permission interaction at its deadline (gofer_ask's
// timeout_sec): no human answered in time, so the job must not stay parked on it. A
// permission interaction keeps the approval gate's own deadline handling. Caller holds the
// entry lock.
func (s *Service) armInteractionExpiry(rec *interactionRec, jobID string) {
	if rec.data.ExpiresAt <= 0 || rec.data.Type == InteractionTypePermission {
		return
	}
	wait := time.Unix(rec.data.ExpiresAt, 0).Sub(s.nowFn())
	if wait < 0 {
		wait = 0
	}
	id := rec.data.ID
	rec.expiry = time.AfterFunc(wait, func() { _ = s.cancelInteraction(jobID, id) })
}

// stopInteractionExpiry stops the deadline timer of an interaction leaving pending.
// Caller holds the entry lock.
func stopInteractionExpiry(rec *interactionRec) {
	if rec.expiry != nil {
		rec.expiry.Stop()
	}
}
