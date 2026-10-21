package job

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// newJobIDRe is the id shape Submit mints now that job ids carry the kind marker
// (gofer-qo3t): job-<date>-<time>-<8 hex>.
var newJobIDRe = regexp.MustCompile(`^job-\d{8}-\d{6}-[0-9a-f]{8}$`)

// TestGenJobIDCarriesKindPrefix pins the new job id shape. Only minting changes:
// an id without the prefix (the older shape) stays valid everywhere, so the
// checks that read an id must accept both (see store.dateSubdir and
// hookrelay.submittedJobRE).
func TestGenJobIDCarriesKindPrefix(t *testing.T) {
	s := &Service{nowFn: func() time.Time { return time.Date(2026, 10, 21, 21, 24, 12, 0, time.UTC) }}
	got := s.genJobID()
	if want := JobIDPrefix + "20261021-212412-"; !strings.HasPrefix(got, want) {
		t.Fatalf("genJobID() = %q, want prefix %q", got, want)
	}
	if !newJobIDRe.MatchString(got) {
		t.Fatalf("genJobID() = %q, want shape %s", got, newJobIDRe)
	}
	if other := s.genJobID(); other == got {
		t.Fatalf("genJobID() returned the same id twice: %q", got)
	}
}
