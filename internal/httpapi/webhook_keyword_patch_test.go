package httpapi

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// TestBuildWebhookPatchKeyword covers gofer-map1's keyword on the config-write path.
// keyword is not a secret, so the view echoes it; the patch still inherits an omitted
// one from the entry it replaces, so a console that does not send the field (or an old
// client) cannot silently drop the operator's keyword on an unrelated save. An
// explicit "" is how the console clears it.
func TestBuildWebhookPatchKeyword(t *testing.T) {
	t.Parallel()
	prev := []config.WebhookConfig{{URL: "https://hooks.example.com/dingtalk", Kind: "dingtalk", Keyword: "gofer"}}

	// Omitted → inherit the matching entry's keyword.
	got := buildWebhookPatch(webhookPatch{URL: "https://hooks.example.com/dingtalk"}, 0, 1, prev, make([]bool, len(prev)))
	if got.Keyword != "gofer" {
		t.Fatalf("keyword = %q, want the inherited one", got.Keyword)
	}

	// Explicit value wins.
	next := "deploy"
	got = buildWebhookPatch(webhookPatch{URL: "https://hooks.example.com/dingtalk", Keyword: &next}, 0, 1, prev, make([]bool, len(prev)))
	if got.Keyword != "deploy" {
		t.Fatalf("keyword = %q, want the explicit one", got.Keyword)
	}

	// Explicit "" clears it.
	clear := ""
	got = buildWebhookPatch(webhookPatch{URL: "https://hooks.example.com/dingtalk", Keyword: &clear}, 0, 1, prev, make([]bool, len(prev)))
	if got.Keyword != "" {
		t.Fatalf("keyword = %q, want it cleared", got.Keyword)
	}

	// A new target inherits nothing, even at the same index (add/remove shape).
	got = buildWebhookPatch(webhookPatch{URL: "https://hooks.example.com/feishu"}, 1, 2, prev, make([]bool, len(prev)))
	if got.Keyword != "" {
		t.Fatalf("new entry inherited keyword %q", got.Keyword)
	}
}
