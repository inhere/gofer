package streaming

import (
	"encoding/json"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// TestACPNormalizerSplitsInjectedRules pins R9: a prompt record becomes a text event
// carrying the caller's own words, with the server-injected rules frame moved into
// `injected` — the same shape GET /v1/jobs/{id}/messages uses. Agent messages are
// never split (nothing is injected into what the agent says).
func TestACPNormalizerSplitsInjectedRules(t *testing.T) {
	frame := "## 必须遵守的规则（gofer 注入，优先于本任务的其它说明）\n\n### house-rules\n- never push\n\n<!-- gofer:rules-end -->"
	cases := []struct {
		name         string
		record       string
		wantKind     string
		wantText     string
		wantInjected string
	}{
		{
			name:         "prompt with an injected frame",
			record:       `{"t":"prompt","text":` + mustJSON(frame+"\n\nFix the login page.") + `}`,
			wantKind:     "prompt",
			wantText:     "Fix the login page.",
			wantInjected: frame,
		},
		{
			name:     "prompt without a frame",
			record:   `{"t":"prompt","text":"Fix the login page."}`,
			wantKind: "prompt",
			wantText: "Fix the login page.",
		},
		{
			// A message that quotes the header is the agent talking; it is not ours to strip.
			name:     "message is never split",
			record:   `{"t":"message","text":` + mustJSON(frame+"\n\nDone.") + `}`,
			wantKind: "message",
			wantText: frame + "\n\nDone.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rec map[string]any
			if err := json.Unmarshal([]byte(tc.record), &rec); err != nil {
				t.Fatal(err)
			}
			var n acpNormalizer
			events := n.record(rec)
			if len(events) != 1 {
				t.Fatalf("events = %v", events)
			}
			event := events[0]
			if event["kind"] != tc.wantKind {
				t.Fatalf("kind = %v, want %s", event["kind"], tc.wantKind)
			}
			if got := event["text"]; got != tc.wantText {
				t.Fatalf("text = %q, want %q", got, tc.wantText)
			}
			got, has := event["injected"]
			if tc.wantInjected == "" {
				if has {
					t.Fatalf("injected = %v, want the field to be absent", got)
				}
				return
			}
			if got != tc.wantInjected {
				t.Fatalf("injected = %q, want %q", got, tc.wantInjected)
			}
			// The two halves reconstruct what was sent, and they agree with the
			// messages read path, which does its own split.
			body, injected := job.SplitInjectedRules(tc.wantInjected + "\n\n" + got.(string))
			if body != got || injected != tc.wantInjected {
				t.Fatalf("split disagreement: body=%q injected=%q", body, injected)
			}
		})
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// The duplicate-name guard: mustJSON's output must stay a plain JSON string (a record is
// decoded from JSON, so an unescaped header would break the case above silently).
