package today

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// Server-side memory doctor (design prime-memory-quality §2.5 / §2.6, P4): the same checks
// as `gofer memory doctor`, run on the server's mirror of each repository tracker. The
// server has no checkout, so path-missing and commit-missing are skipped and the repo's
// prime.doctor.suppress is unknown; a memory's own doctor_ignore still applies.

// memoryFindingContentRunes caps the memory body a finding carries for the steward.
const memoryFindingContentRunes = 1500

// MirrorMemories decodes the live (non-tombstone) mirrored memories.
func MirrorMemories(recs []jobstore.TrackerRecord) []tracker.Memory {
	out := make([]tracker.Memory, 0, len(recs))
	for _, r := range recs {
		if r.Deleted {
			continue
		}
		var m tracker.Memory
		if json.Unmarshal(r.Body, &m) != nil {
			continue
		}
		if m.Key == "" {
			m.Key = r.ID
		}
		out = append(out, m)
	}
	return out
}

// DiagnoseMirror runs the server-side doctor over mirrored records and returns the
// findings per key (keys without findings are absent).
func DiagnoseMirror(recs []jobstore.TrackerRecord, now time.Time) map[string][]tracker.DoctorFinding {
	report := tracker.DiagnoseMemories(MirrorMemories(recs), tracker.DoctorOptions{Now: now})
	out := make(map[string][]tracker.DoctorFinding, len(report.Memories))
	for _, e := range report.Memories {
		out[e.Key] = e.Findings
	}
	return out
}

// MemoryFinding is one flagged mirrored memory (GET /v1/memory-findings,
// gofer_memory_findings).
type MemoryFinding struct {
	TrackerID  string                  `json:"tracker_id"`
	ProjectKey string                  `json:"project_key,omitempty"`
	Key        string                  `json:"key"`
	Kind       string                  `json:"kind"`
	Summary    string                  `json:"summary,omitempty"`
	UpdatedAt  string                  `json:"updated_at,omitempty"`
	Age        string                  `json:"age,omitempty"`
	Findings   []tracker.DoctorFinding `json:"findings"`
	Content    string                  `json:"content,omitempty"`
}

// MemoryFindingsQuery filters MemoryFindings.
type MemoryFindingsQuery struct {
	TrackerID string
}

// MemoryFindings runs the server-side doctor over every mirrored repository (or one).
func (s *Service) MemoryFindings(q MemoryFindingsQuery) ([]MemoryFinding, error) {
	st := s.d.Store
	repos, err := st.ListTrackerRepos()
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	out := []MemoryFinding{}
	for _, repo := range repos {
		if q.TrackerID != "" && repo.TrackerID != q.TrackerID {
			continue
		}
		recs, err := st.ListTrackerMemories(repo.TrackerID, 0)
		if err != nil {
			return nil, err
		}
		items := MirrorMemories(recs)
		byKey := make(map[string]tracker.Memory, len(items))
		for _, m := range items {
			byKey[m.Key] = m
		}
		report := tracker.DiagnoseMemories(items, tracker.DoctorOptions{Now: now})
		for _, e := range report.Memories {
			out = append(out, MemoryFinding{TrackerID: repo.TrackerID, ProjectKey: repo.ProjectKey, Key: e.Key, Kind: e.Kind, Summary: e.Summary,
				UpdatedAt: e.UpdatedAt, Age: tracker.AgeText(e.UpdatedAt, now), Findings: e.Findings,
				Content: capRunes(strings.TrimSpace(byKey[e.Key].Content), memoryFindingContentRunes)})
		}
	}
	return out, nil
}
