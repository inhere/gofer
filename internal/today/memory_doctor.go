package today

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/steward"
	"github.com/inhere/gofer/internal/tracker"
)

// Server-side memory doctor (design prime-memory-quality §2.5 / §2.6, P4): the same checks
// as `gofer memory doctor`, run on the server's mirror of each repository tracker. The
// server has no checkout, so path-missing and commit-missing are skipped and the repo's
// prime.doctor.suppress is unknown; a memory's own doctor_ignore still applies.

// memoryFindingContentRunes caps the memory body a finding carries for the steward.
const memoryFindingContentRunes = 1500

// slugActions maps a finding to the cleanup it naturally calls for.
var slugActions = map[string]string{
	tracker.DoctorHandoffExpired: MemoryActArchive,
	tracker.DoctorNoteStale:      MemoryActArchive,
	tracker.DoctorSummaryMissing: MemoryActSummary,
	tracker.DoctorDuplicate:      MemoryActMerge,
}

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
	// Actions are the cleanups the findings call for that are still open: not pending as a
	// suggestion and not dismissed within the cooldown.
	Actions []string `json:"actions"`
	Content string   `json:"content,omitempty"`
}

// MemoryFindingsQuery filters MemoryFindings.
type MemoryFindingsQuery struct {
	TrackerID string
	// All keeps findings whose actions are already pending or were dismissed recently.
	All bool
}

// MemoryFindings runs the server-side doctor over every mirrored repository (or one).
func (s *Service) MemoryFindings(q MemoryFindingsQuery) ([]MemoryFinding, error) {
	st := s.d.Store
	repos, err := st.ListTrackerRepos()
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	pending, err := s.pendingMemoryActions()
	if err != nil {
		return nil, err
	}
	cooldownSince := now.Add(-MemorySuggestCooldown).Unix()
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
			f := MemoryFinding{TrackerID: repo.TrackerID, ProjectKey: repo.ProjectKey, Key: e.Key, Kind: e.Kind, Summary: e.Summary,
				UpdatedAt: e.UpdatedAt, Age: tracker.AgeText(e.UpdatedAt, now), Findings: e.Findings, Actions: []string{}}
			seen := map[string]bool{}
			for _, fd := range e.Findings {
				act := slugActions[fd.Slug]
				if act == "" || seen[act] {
					continue
				}
				seen[act] = true
				if pending[memoryActionKey(repo.TrackerID, e.Key, act)] {
					continue
				}
				dismissed, err := st.MemorySuggestionDismissedSince(repo.TrackerID, e.Key, act, cooldownSince)
				if err != nil {
					return nil, err
				}
				if !dismissed {
					f.Actions = append(f.Actions, act)
				}
			}
			if len(f.Actions) == 0 && !q.All {
				continue
			}
			f.Content = capRunes(strings.TrimSpace(byKey[e.Key].Content), memoryFindingContentRunes)
			out = append(out, f)
		}
	}
	return out, nil
}

func memoryActionKey(trackerID, key, action string) string {
	return trackerID + "\x00" + key + "\x00" + action
}

func (s *Service) pendingMemoryActions() (map[string]bool, error) {
	rows, err := s.d.Store.ListMemorySuggestions(jobstore.MemorySuggestPending)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[memoryActionKey(r.TrackerID, r.MemoryKey, r.Action)] = true
	}
	return out, nil
}

// MemoryHygiene is the steward's trigger for the memory step of a review: the open
// findings (one signature per tracker/key/action) and how many suggestions today still
// allows. A review only wakes for signatures it has not been shown (steward side).
func (s *Service) MemoryHygiene() (steward.MemoryHygiene, error) {
	findings, err := s.MemoryFindings(MemoryFindingsQuery{})
	if err != nil {
		return steward.MemoryHygiene{}, err
	}
	used, err := s.d.Store.CountMemorySuggestionsSince(startOfDay(s.d.Now()).Unix())
	if err != nil {
		return steward.MemoryHygiene{}, err
	}
	h := steward.MemoryHygiene{Findings: len(findings), Remaining: MemorySuggestDailyCap - used}
	if h.Remaining < 0 {
		h.Remaining = 0
	}
	for _, f := range findings {
		for _, a := range f.Actions {
			h.Signatures = append(h.Signatures, f.TrackerID+"/"+f.Key+"/"+a)
		}
		if len(h.Lines) < steward.MemoryHygieneLines {
			slugs := make([]string, 0, len(f.Findings))
			for _, fd := range f.Findings {
				slugs = append(slugs, fd.Slug)
			}
			h.Lines = append(h.Lines, f.TrackerID+" · "+f.Key+"（"+f.Kind+"）："+strings.Join(slugs, ", "))
		}
	}
	sort.Strings(h.Signatures)
	return h, nil
}
