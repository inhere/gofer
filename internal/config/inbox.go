package config

import (
	"slices"
	"strconv"
	"strings"
)

// inboxKindEvents maps an inbox kind to the notify event keys that announce it. It is the
// config-side half of the Inbox / notify alignment: internal/notify imports this package
// (not the reverse), so the keys are literals here and internal/inbox's coverage tests
// pin them to notify.EventCatalog in both directions. A kind absent from the map has no
// notify event (decision, mail_held, ...). One event may announce several kinds
// (job.terminal is a fyi job.terminal or an action job.failed, told apart by the job).
var inboxKindEvents = map[string][]string{
	"interaction":  {"interaction.created"},
	"relay":        {"session.awaiting_reply"},
	"approval":     {"job.awaiting_approval"},
	"work":         {"work.needs_me", "work.remind"},
	"plan_blocked": {"plan.blocked"},
	"review":       {"job.needs_review"},
	"job.failed":   {"job.terminal"},
	"job.terminal": {"job.terminal"},
	"work.digest":  {"work.digest"},
}

// InboxKinds lists every inbox kind of the built-in policy, default column order.
func InboxKinds() []string {
	var out []string
	for _, r := range DefaultInboxRules() {
		out = append(out, r.Kinds...)
	}
	return out
}

// InboxDefaultColumn is the built-in column of an inbox kind, "" for an unknown kind.
func InboxDefaultColumn(kind string) string {
	for _, r := range DefaultInboxRules() {
		if slices.Contains(r.Kinds, kind) {
			return r.Column
		}
	}
	return ""
}

// InboxKindEvents returns the notify events that announce kind (a fresh slice, maybe empty).
func InboxKindEvents(kind string) []string { return slices.Clone(inboxKindEvents[kind]) }

// InboxEventKinds returns the inbox kinds a notify event announces, in InboxKinds order;
// empty means the event never reaches the inbox.
func InboxEventKinds(event string) []string {
	var out []string
	for _, k := range InboxKinds() {
		if slices.Contains(inboxKindEvents[k], event) {
			out = append(out, k)
		}
	}
	return out
}

// InboxEvents returns every notify event that reaches the inbox, sorted.
func InboxEvents() []string {
	var out []string
	for _, evs := range inboxKindEvents {
		for _, e := range evs {
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	slices.Sort(out)
	return out
}

// EffectiveKinds is the kinds a rule selects: Kinds plus the kinds its Events announce,
// deduplicated. A rule with neither Kinds nor Events selects every kind (see HasSelector).
func (r InboxRule) EffectiveKinds() []string {
	out := slices.Clone(r.Kinds)
	for _, e := range r.Events {
		for _, k := range InboxEventKinds(e) {
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// HasSelector reports whether the rule narrows by kinds or events; without one it matches
// every kind.
func (r InboxRule) HasSelector() bool { return len(r.Kinds)+len(r.Events) > 0 }

// InboxRulesSummary renders one line per effective rule, highest priority first, for
// `config validate`: "id column priority=N kinds=[..] projects=[..]". custom=false means
// the built-in table is in effect.
func InboxRulesSummary(in *InboxConfig) (lines []string, custom bool) {
	rules := DefaultInboxRules()
	if in != nil && len(in.Rules) > 0 {
		rules, custom = slices.Clone(in.Rules), true
	}
	slices.SortStableFunc(rules, func(a, b InboxRule) int { return b.Priority - a.Priority })
	for _, r := range rules {
		line := r.ID + " -> " + r.Column + " priority=" + strconv.Itoa(r.Priority)
		if r.HasSelector() {
			line += " kinds=" + "[" + strings.Join(r.EffectiveKinds(), ",") + "]"
		} else {
			line += " kinds=*"
		}
		if len(r.Projects) > 0 {
			line += " projects=" + "[" + strings.Join(r.Projects, ",") + "]"
		}
		lines = append(lines, line)
	}
	return lines, custom
}
