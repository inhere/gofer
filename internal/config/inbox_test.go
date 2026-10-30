package config

import "testing"

func TestValidateInboxRules(t *testing.T) {
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "bad", Kinds: []string{"review"}, Priority: 1}}}); err == nil {
		t.Fatal("invalid column accepted")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Kinds: []string{"not-a-kind"}, Priority: 1}}}); err == nil {
		t.Fatal("invalid kind accepted")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Kinds: []string{"review"}, Priority: 1}, {ID: "x", Column: "fyi", Kinds: []string{"review"}, Priority: 2}}}); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "a", Column: "action", Kinds: []string{"review"}, Priority: 5}, {ID: "b", Column: "fyi", Kinds: []string{"review"}, Priority: 5}}}); err == nil {
		t.Fatal("ambiguous priority accepted")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Events: []string{"job.terminal"}, Priority: 1}}}); err == nil {
		t.Fatal("events accepted although classification does not read it")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Kinds: []string{"review"}, Priority: 1, IncludeExec: true}}}); err == nil {
		t.Fatal("include_exec accepted although classification does not read it")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Kinds: []string{"job.failed"}, Priority: 1}}}); err != nil {
		t.Fatalf("job.failed kind rejected: %v", err)
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Kinds: []string{"review"}, Priority: 1}}}); err != nil {
		t.Fatal(err)
	}
}

func TestInboxConfigEnabledDefaultsOn(t *testing.T) {
	var nilCfg *InboxConfig
	off := false
	on := true
	for name, tc := range map[string]struct {
		cfg  *InboxConfig
		want bool
	}{
		"nil config":     {nilCfg, true},
		"unset enabled":  {&InboxConfig{Rules: []InboxRule{{ID: "x"}}}, true},
		"explicit true":  {&InboxConfig{Enabled: &on}, true},
		"explicit false": {&InboxConfig{Enabled: &off}, false},
	} {
		if got := tc.cfg.IsEnabled(); got != tc.want {
			t.Errorf("%s: IsEnabled = %v, want %v", name, got, tc.want)
		}
	}
}
