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
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Events: []string{"not-an-event"}, Priority: 1}}}); err == nil {
		t.Fatal("invalid event accepted")
	}
	if err := ValidateInbox(&InboxConfig{Rules: []InboxRule{{ID: "x", Column: "action", Kinds: []string{"review"}, Priority: 1}}}); err != nil {
		t.Fatal(err)
	}
}
