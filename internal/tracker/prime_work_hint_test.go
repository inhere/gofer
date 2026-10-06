package tracker

import (
	"strings"
	"testing"
)

// W1: the SessionStart prime carries exactly one short line about `gofer work report`,
// inside the fixed header (so the byte budget logic never drops it).
func TestPrimeCarriesOneLineWorkReportHint(t *testing.T) {
	s, _, err := Init(t.TempDir(), "prime-work", true)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(body, "gofer work report"); n != 1 {
		t.Fatalf("prime mentions `gofer work report` %d times, want 1:\n%s", n, body)
	}
	if strings.Contains(WorkPrimeHint, "\n") || len([]rune(WorkPrimeHint)) > 120 {
		t.Fatalf("hint must stay one short line: %q", WorkPrimeHint)
	}
}
