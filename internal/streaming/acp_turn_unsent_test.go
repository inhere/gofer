package streaming

import "testing"

// TestACPNormalizerForwardsTurnUnsent: the "message not sent" record a resident session
// writes when its turn could not take the directory lock reaches the stream with its
// note and the original message, so the timeline can show it and offer a resend.
func TestACPNormalizerForwardsTurnUnsent(t *testing.T) {
	var n acpNormalizer
	events := n.record(map[string]any{"t": "turn_unsent", "text": "not sent", "prompt": "run the tests"})
	if len(events) != 1 {
		t.Fatalf("events = %v", events)
	}
	ev := events[0]
	if ev["kind"] != "turn_unsent" || ev["text"] != "not sent" || ev["prompt"] != "run the tests" {
		t.Fatalf("event = %v", ev)
	}
}
