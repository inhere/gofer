package memorysynthesis

import (
	"strings"
	"testing"
)

func TestBuildScrubsDeduplicatesAndDigests(t *testing.T) {
	items := []Item{
		{ID: "2", Kind: "job", OccurredAt: "2026-10-28T02:00:00Z", Title: "done", Text: "token=secret-value", SourceRef: "job:2#report"},
		{ID: "1", Kind: "job", OccurredAt: "2026-10-28T01:00:00Z", Title: "same", Text: "fact", SourceRef: "job:1#report"},
		{ID: "3", Kind: "job", OccurredAt: "2026-10-28T03:00:00Z", Title: "same", Text: "fact", SourceRef: "job:3#report"},
	}
	a, err := Build("proj", "2026-10-28", "+08:00", items, Options{MaxInputBytes: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Items) != 2 || a.Items[0].SourceRef != "job:1#report" {
		t.Fatalf("items=%+v", a.Items)
	}
	if a.Items[0].SourceRefs == nil || len(a.Items[0].SourceRefs) != 1 || a.Items[0].SourceRefs[0] != "job:3#report" {
		t.Fatalf("refs=%v", a.Items[0].SourceRefs)
	}
	if a.Items[1].Text != "[redacted]" || a.Items[1].Sensitivity != "redacted" {
		t.Fatalf("scrub=%+v", a.Items[1])
	}
	b, err := Build("proj", "2026-10-28", "+08:00", items, Options{MaxInputBytes: 24000})
	if err != nil || a.InputDigest != b.InputDigest {
		t.Fatalf("digest not stable: %q %q %v", a.InputDigest, b.InputDigest, err)
	}
}

func TestBuildBudgetAndPerKindLimit(t *testing.T) {
	items := []Item{{Kind: "work", SourceRef: "a", Text: "12345"}, {Kind: "work", SourceRef: "b", Text: "67890"}}
	got, err := Build("p", "2026-10-28", "UTC", items, Options{MaxInputBytes: 280, MaxItemsPerKind: 1, MaxRunesPerText: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Omitted.Items == 0 {
		t.Fatalf("got=%+v", got)
	}
}

// TestBuildScrubsPrivacyCategories pins design section 8: every category is replaced in
// both title and text, never reaches the digest input, and marks the item redacted.
func TestBuildScrubsPrivacyCategories(t *testing.T) {
	cases := map[string]struct{ text, secret string }{
		"openai-style key":    {"used sk-abcdef1234567890abcdef in the log", "sk-abcdef1234567890abcdef"},
		"github token":        {"pushed with ghp_abcdefghijklmnopqrstuvwx1234", "ghp_abcdefghijklmnopqrstuvwx1234"},
		"authorization basic": {"Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA"},
		"authorization token": {"curl -H 'Authorization: Bearer abcDEF123456xyz' https://x", "abcDEF123456xyz"},
		"bare bearer":         {"header was Bearer abcDEF123456xyz", "abcDEF123456xyz"},
		"private key block":   {"key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\nabc\n-----END RSA PRIVATE KEY-----\nafter", "MIIEowIBAAKCAQEA"},
		"env assignment":      {"ran with DATABASE_URL=postgres://u:pw@h/db ok", "postgres://u:pw@h/db"},
		"argv token flag":     {"gofer login --token s3cr3tvalue --verbose", "s3cr3tvalue"},
		"argv equals flag":    {"tool --password=hunter2hunter2", "hunter2hunter2"},
		"json secret":         {`{"api_key": "abcd1234efgh"}`, "abcd1234efgh"},
		"mail address":        {"Mail body from someone.name@example.org said hi", "someone.name@example.org"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Build("p", "2026-10-28", "UTC", []Item{{Kind: "k", SourceRef: "r", Title: tc.text, Text: tc.text}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Items) != 1 || got.Items[0].Sensitivity != "redacted" {
				t.Fatalf("not marked redacted: %+v", got.Items)
			}
			if strings.Contains(got.Items[0].Text, tc.secret) || strings.Contains(got.Items[0].Title, tc.secret) {
				t.Fatalf("secret survived: %+v", got.Items[0])
			}
			if len(got.Omitted.Reasons) != 1 || got.Omitted.Reasons[0] != "secret" {
				t.Fatalf("reasons=%v", got.Omitted.Reasons)
			}
		})
	}
}

// TestBuildScrubsBeforeTruncating: a secret straddling the text limit used to be cut first
// and scrubbed second, leaving an unrecognisable but still sensitive prefix.
func TestBuildScrubsBeforeTruncating(t *testing.T) {
	text := strings.Repeat("a", 96) + " sk-" + strings.Repeat("Z", 40)
	got, err := Build("p", "2026-10-28", "UTC", []Item{{Kind: "k", SourceRef: "r", Text: text}}, Options{MaxRunesPerText: 100})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Items[0].Text, "sk-") || strings.Contains(got.Items[0].Text, "ZZZ") {
		t.Fatalf("truncated secret prefix leaked: %q", got.Items[0].Text)
	}
	// An unterminated private key block (cut by an upstream limit) is removed as a whole.
	got, err = Build("p", "2026-10-28", "UTC", []Item{{Kind: "k", SourceRef: "r", Text: "x -----BEGIN PRIVATE KEY-----\nMIIEvQIBADAN"}}, Options{})
	if err != nil || strings.Contains(got.Items[0].Text, "MIIE") {
		t.Fatalf("unterminated key leaked: %+v err=%v", got.Items, err)
	}
}
