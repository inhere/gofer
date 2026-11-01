package config

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateInboxRulesNamesTheEntry(t *testing.T) {
	ok := InboxRule{ID: "ok", Column: "action", Kinds: []string{"review"}, Priority: 5}
	cases := map[string]struct {
		rules []InboxRule
		want  string
	}{
		"empty id":        {[]InboxRule{{Column: "action", Priority: 1}}, "server.inbox.rules[0]"},
		"duplicate id":    {[]InboxRule{ok, ok}, `duplicate id "ok"`},
		"bad column":      {[]InboxRule{{ID: "c", Column: "later", Priority: 1}}, "rules[c]: unknown column"},
		"unknown kind":    {[]InboxRule{{ID: "k", Column: "fyi", Kinds: []string{"nope"}, Priority: 1}}, "rules[k].kinds"},
		"unknown event":   {[]InboxRule{{ID: "e", Column: "fyi", Events: []string{"nope.event"}, Priority: 1}}, "rules[e].events"},
		"non-inbox event": {[]InboxRule{{ID: "e", Column: "fyi", Events: []string{"job.reviewed"}, Priority: 1}}, "rules[e].events"},
		"duplicate kind":  {[]InboxRule{{ID: "d", Column: "fyi", Kinds: []string{"review", "review"}, Priority: 1}}, "rules[d].kinds: duplicate"},
		"duplicate event": {[]InboxRule{{ID: "d", Column: "fyi", Events: []string{"job.needs_review", "job.needs_review"}, Priority: 1}}, "rules[d].events: duplicate"},
		"duplicate proj":  {[]InboxRule{{ID: "d", Column: "fyi", Projects: []string{"p", "p"}, Priority: 1}}, "rules[d].projects: duplicate"},
		"zero priority":   {[]InboxRule{{ID: "z", Column: "fyi"}}, "rules[z]: priority"},
		"kind vs event":   {[]InboxRule{{ID: "a", Column: "action", Kinds: []string{"review"}, Priority: 3}, {ID: "b", Column: "fyi", Events: []string{"job.needs_review"}, Priority: 3}}, `overlapping rules "a" and "b"`},
		"selectorless vs": {[]InboxRule{{ID: "a", Column: "action", Priority: 3}, {ID: "b", Column: "fyi", Kinds: []string{"comment"}, Priority: 3}}, "overlapping"},
		"include exec":    {[]InboxRule{{ID: "x", Column: "action", Kinds: []string{"review"}, Priority: 1, IncludeExec: true}}, "rules[x]: include_exec"},
	}
	for name, tc := range cases {
		err := ValidateInbox(&InboxConfig{Rules: tc.rules})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, tc.want)
		}
	}
	good := []InboxRule{
		{ID: "a", Column: "action", Events: []string{"job.needs_review", "plan.blocked"}, Priority: 3},
		{ID: "b", Column: "fyi", Kinds: []string{"comment"}, Priority: 3},
		{ID: "c", Column: "fyi", Kinds: []string{"review"}, Projects: []string{"p"}, Priority: 4},
	}
	if err := ValidateInbox(&InboxConfig{Rules: good}); err != nil {
		t.Fatalf("valid rules rejected: %v", err)
	}
}

func TestInboxEventKindMapping(t *testing.T) {
	if got := InboxEventKinds("job.terminal"); !slices.Equal(got, []string{"job.failed", "job.terminal"}) {
		t.Fatalf("job.terminal kinds = %v", got)
	}
	if got := InboxEventKinds("job.reviewed"); len(got) != 0 {
		t.Fatalf("job.reviewed kinds = %v", got)
	}
	r := InboxRule{Kinds: []string{"decision"}, Events: []string{"job.needs_review", "job.terminal"}}
	if got := r.EffectiveKinds(); !slices.Equal(got, []string{"decision", "review", "job.failed", "job.terminal"}) {
		t.Fatalf("effective kinds = %v", got)
	}
	for _, k := range InboxKinds() {
		if InboxDefaultColumn(k) == "" {
			t.Fatalf("kind %s has no default column", k)
		}
	}
	// Every mapped kind must be a real kind (a typo in the map would silently drop it).
	for k := range inboxKindEvents {
		if !slices.Contains(InboxKinds(), k) {
			t.Fatalf("inboxKindEvents names unknown kind %q", k)
		}
	}
}

func TestInboxRulesSummary(t *testing.T) {
	lines, custom := InboxRulesSummary(nil)
	if custom || len(lines) != 2 || !strings.HasPrefix(lines[0], "action -> action priority=100 kinds=[interaction,") {
		t.Fatalf("default summary = %v custom=%v", lines, custom)
	}
	lines, custom = InboxRulesSummary(&InboxConfig{Rules: []InboxRule{
		{ID: "lo", Column: "fyi", Priority: 1},
		{ID: "hi", Column: "action", Events: []string{"job.needs_review"}, Projects: []string{"p"}, Priority: 9},
	}})
	want := []string{"hi -> action priority=9 kinds=[review] projects=[p]", "lo -> fyi priority=1 kinds=*"}
	if !custom || !slices.Equal(lines, want) {
		t.Fatalf("custom summary = %v custom=%v", lines, custom)
	}
}
