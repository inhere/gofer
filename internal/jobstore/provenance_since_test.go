package jobstore

import (
	"path/filepath"
	"testing"
)

// TestProvenanceSinceIsSetOnceOnFirstOpen: the marker is the first open with provenance
// stamping and survives reopening unchanged.
func TestProvenanceSinceIsSetOnceOnFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := s.ProvenanceSince()
	if first <= 0 {
		t.Fatalf("ProvenanceSince = %d, want the first-open time", first)
	}
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := s2.ProvenanceSince(); got != first {
		t.Fatalf("reopened ProvenanceSince = %d, want %d", got, first)
	}
}
