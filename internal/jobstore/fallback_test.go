package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestJobFallbackColumnsRoundTrip: the failover columns (SUP-01 P3) are persisted
// job properties — the source row keeps its failure class and the pointer to the
// job that took over, the fallback job keeps where it came from and which agent the
// caller actually asked for, and a row that never took part in a transfer reads back
// empty (the columns' COALESCE default) rather than looking like a fallback.
func TestJobFallbackColumnsRoundTrip(t *testing.T) {
	s := openTest(t)

	src := sampleJob("fb-src", "alpha", 2000)
	src.Status, src.EndedAt = "failed", 2100
	src.FailureClass = "transient"
	src.FellBackFrom = "fb-root"
	src.FellBackTo = "fb-next"
	src.RequestedAgent = "codex"
	src.FallbackJSON = `{"candidates":["omp","claude"],"depth":1}`
	assert.NoErr(t, s.UpsertJob(src))

	got, ok, err := s.GetJob("fb-src")
	assert.NoErr(t, err)
	assert.True(t, ok)
	if got.FailureClass != "transient" || got.FellBackFrom != "fb-root" || got.FellBackTo != "fb-next" {
		t.Fatalf("fallback columns = class %q from %q to %q, want transient/fb-root/fb-next",
			got.FailureClass, got.FellBackFrom, got.FellBackTo)
	}
	if got.RequestedAgent != "codex" {
		t.Fatalf("requested_agent = %q, want codex", got.RequestedAgent)
	}
	if got.FallbackJSON != src.FallbackJSON {
		t.Fatalf("fallback_json = %q, want %q", got.FallbackJSON, src.FallbackJSON)
	}

	// A plain job (no transfer involved) must not read back as a fallback link, and a
	// later upsert of the same row must overwrite the columns in place.
	plain := sampleJob("fb-plain", "alpha", 2001)
	assert.NoErr(t, s.UpsertJob(plain))
	p, _, err := s.GetJob("fb-plain")
	assert.NoErr(t, err)
	if p.FailureClass != "" || p.FellBackFrom != "" || p.FellBackTo != "" || p.RequestedAgent != "" || p.FallbackJSON != "" {
		t.Fatalf("a job outside a transfer must read back empty, got %+v", p)
	}

	got.FellBackTo = ""
	got.FailureClass = "other"
	assert.NoErr(t, s.UpsertJob(got))
	again, _, err := s.GetJob("fb-src")
	assert.NoErr(t, err)
	if again.FellBackTo != "" || again.FailureClass != "other" {
		t.Fatalf("re-upsert must overwrite the columns, got to=%q class=%q", again.FellBackTo, again.FailureClass)
	}
}
