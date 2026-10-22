package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// POST /v1/mail with dry_run resolves the addresses and answers how many letters a real send would
// write (and how many a selector would skip), without storing one (gofer-ndy5; G056: the flag
// rides the existing route).
func TestMailBroadcastDryRunStoresNothing(t *testing.T) {
	e := newMailEnv(t)
	e.grant()

	status, body := e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{
		"to": []string{"session:" + e.sA, "session:" + e.sSup}, "body": "预览", "dry_run": true,
	})
	if status != http.StatusOK {
		t.Fatalf("dry run = %d (%s)", status, body)
	}
	var preview mailPreviewView
	if err := json.Unmarshal([]byte(body), &preview); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if !preview.DryRun || preview.Delivered != 2 || preview.Skipped != 0 || len(preview.Recipients) != 2 {
		t.Fatalf("preview = %+v, want 2 deliverable recipients", preview)
	}
	for _, rc := range preview.Recipients {
		if rc.MailID != 0 || rc.ID == "" {
			t.Fatalf("preview recipient = %+v, want an id and no letter", rc)
		}
	}

	// Nothing was written.
	rows, err := e.s.jobs.Meta().ListMail(jobstore.ListMailOpts{})
	if err != nil {
		t.Fatalf("list mail: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("dry run stored %d letters, want none", len(rows))
	}

	// A real send of the same addresses works and writes the two copies.
	status, body = e.req(t, mailTokAlice, "", "POST", "/v1/mail", map[string]any{
		"to": []string{"session:" + e.sA, "session:" + e.sSup}, "body": "预览",
	})
	if status != http.StatusOK {
		t.Fatalf("broadcast = %d (%s)", status, body)
	}
	if sent := decodeSent(t, body); sent.Delivered != 2 {
		t.Fatalf("broadcast delivered %d, want 2", sent.Delivered)
	}
}
