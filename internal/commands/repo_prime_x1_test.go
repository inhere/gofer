package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/inhere/gofer/internal/tracker"
)

func TestRepoStatusShowsPrimeSize(t *testing.T) {
	root := t.TempDir()
	s, _, err := tracker.Init(root, "prime", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateMemories(func([]tracker.Memory) ([]tracker.Memory, error) {
		return []tracker.Memory{{Key: "large", Content: strings.Repeat("x", 10000), Tags: []string{"prime"}, UpdatedAt: "2026-09-30"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	out := trackerRunOK(t, root, "repo", "status")
	if !strings.Contains(out, "prime_bytes:") || !strings.Contains(out, "prime_truncated: true") {
		t.Fatalf("repo status must show prime size/truncation: %q", out)
	}
	var status tracker.RepoStatus
	if err := json.Unmarshal([]byte(trackerRunOK(t, root, "repo", "status", "--json")), &status); err != nil {
		t.Fatal(err)
	}
	if status.PrimeBytes <= tracker.PrimeMaxBytes || !status.PrimeTruncated {
		t.Fatalf("status JSON prime estimate=%d truncated=%v", status.PrimeBytes, status.PrimeTruncated)
	}
}

func TestPrimeConfigTogglesServerSections(t *testing.T) {
	root := t.TempDir()
	s, _, err := tracker.Init(root, "prime", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		t.Fatal(err)
	}
	off := false
	cfg.Prime.ScopedMemory = &off
	cfg.Prime.Handoff = &off
	cfg.Prime.Focus = &off
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, "config.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := s.Prime()
	if err != nil {
		t.Fatal(err)
	}
	got, err := primeWithServerContext(s, "", "claude")
	if err != nil || got != want {
		t.Fatalf("disabled server sections: got=%q want=%q err=%v", got, want, err)
	}
}
