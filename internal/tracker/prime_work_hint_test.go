package tracker

import (
	"strings"
	"testing"
)

// The `gofer work report` line is conditional: absent by default, exactly one short
// line inside the fixed header (so the byte budget never drops it) when WorkHint is set.
func TestPrimeWorkReportHintIsConditional(t *testing.T) {
	s, _, err := Init(t.TempDir(), "prime-work", true)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "gofer work report") {
		t.Fatalf("prime carries the work-report hint without WorkHint:\n%s", body)
	}
	opts := s.defaultPrimeOptions()
	opts.WorkHint = true
	body, err = s.PrimeWith(opts)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(body, "gofer work report"); n != 1 {
		t.Fatalf("prime mentions `gofer work report` %d times, want 1:\n%s", n, body)
	}
	if head, _, _ := strings.Cut(body, TrackerPrimeHint); !strings.Contains(head, WorkPrimeHint) {
		t.Fatalf("hint must sit in the fixed header:\n%s", body)
	}
	if strings.Contains(WorkPrimeHint, "\n") || len([]rune(WorkPrimeHint)) > 120 {
		t.Fatalf("hint must stay one short line: %q", WorkPrimeHint)
	}
}
