package today

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// Memory hygiene cards (design prime-memory-quality §2.6, P4). The steward proposes ONE
// change to one repo-tracker memory of the server mirror; it becomes a `memory` card with
// 「采纳」/「忽略」. Adopting applies the change to the SERVER copy through the tracker
// mirror write path, so every clone picks it up on its next `repo sync`:
//
//   - archive  → an archive tombstone (deleted_by "archive:<actor>"): clones move their
//     local copy to memories-archive.jsonl (older clients just delete it);
//   - merge    → the target's content becomes payload.content (or target + source when
//     empty), then the source gets an archive tombstone (clones archive it, not drop it);
//   - kind / summary / when → a field patch (when: keywords are added to the existing ones).
//
// Adoption is only valid against the memory the steward looked at: the live rev must still
// be the suggestion's BaseRev (and, for merge, the target's rev its TargetRev); every write
// is a compare-and-set on that rev. Otherwise the suggestion goes stale
// (ErrMemorySuggestionStale, 409) and the steward proposes again from the new content.
//
// A dedicated kind (not `suggestion`): `suggestion` cards are work-item field suggestions
// keyed by work item + field, and their actions call the work-item API.

// Memory suggestion actions.
const (
	MemoryActArchive = "archive"
	MemoryActMerge   = "merge"
	MemoryActKind    = "kind"
	MemoryActSummary = "summary"
	MemoryActWhen    = "when"
)

// Limits.
const (
	// MemorySuggestDailyCap is how many memory suggestions may be created per local day.
	MemorySuggestDailyCap = 5
	// MemorySuggestCooldown keeps a dismissed (key, action) from being proposed again.
	MemorySuggestCooldown   = 30 * 24 * time.Hour
	memoryReasonRunes       = 120
	memoryMergeContentRunes = 8000
	memoryWhenMaxKeywords   = 8
	memoryKeywordRunes      = 32
	memoryCardContentRunes  = 600
)

var (
	// ErrInvalidMemorySuggestion is a malformed suggestion.
	ErrInvalidMemorySuggestion = errors.New("today: invalid memory suggestion")
	// ErrMemoryNotFound: the memory (or merge target) is not live in the server mirror.
	ErrMemoryNotFound = errors.New("today: memory not found in the server mirror")
	// ErrMemorySuggestCooldown: the same key + action was dismissed within the cooldown.
	ErrMemorySuggestCooldown = errors.New("today: dismissed recently")
	// ErrMemorySuggestCap: today's memory suggestions are used up.
	ErrMemorySuggestCap = errors.New("today: daily memory suggestion cap reached")
	// ErrMemorySuggestionStale: the memory (or merge target) changed after the suggestion
	// was made; the suggestion is marked stale instead of applied.
	ErrMemorySuggestionStale = errors.New("记忆在建议之后被改过，请重新整理")
)

var memoryActLabels = map[string]string{
	MemoryActArchive: "归档", MemoryActMerge: "合并", MemoryActKind: "改类型", MemoryActSummary: "补摘要", MemoryActWhen: "补触发词",
}

// ValidMemoryAction reports whether action is a known memory suggestion action.
func ValidMemoryAction(action string) bool { _, ok := memoryActLabels[action]; return ok }

// MemoryPayload is the proposed value of a memory suggestion (only the field of its action).
type MemoryPayload struct {
	// Into is the merge target key.
	Into string `json:"into,omitempty"`
	// Content is the merged body written to the target (merge; empty = target + source).
	Content  string   `json:"content,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

// MemorySuggestInput is POST /v1/memory-suggestions and gofer_memory_suggest.
type MemorySuggestInput struct {
	TrackerID string        `json:"tracker_id"`
	Key       string        `json:"key"`
	Action    string        `json:"action"`
	Payload   MemoryPayload `json:"payload,omitempty"`
	Reason    string        `json:"reason"`
}

// MemorySuggestion is the API view of one suggestion.
type MemorySuggestion struct {
	ID         int64         `json:"id"`
	TrackerID  string        `json:"tracker_id"`
	ProjectKey string        `json:"project_key,omitempty"`
	Key        string        `json:"key"`
	Action     string        `json:"action"`
	Payload    MemoryPayload `json:"payload"`
	// Text is the one-line proposal (「建议归档」「建议合并到 x」…).
	Text      string `json:"text"`
	Reason    string `json:"reason"`
	State     string `json:"state"`
	By        string `json:"by,omitempty"`
	CreatedAt int64  `json:"created_at"`
	DecidedAt int64  `json:"decided_at,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	Note      string `json:"note,omitempty"`
}

// MemoryCard is the 「详情」 of a memory card: the proposal and the memory as it is now.
type MemoryCard struct {
	TrackerID      string        `json:"tracker_id"`
	Key            string        `json:"key"`
	Action         string        `json:"action"`
	Payload        MemoryPayload `json:"payload"`
	CurrentKind    string        `json:"current_kind,omitempty"`
	CurrentSummary string        `json:"current_summary,omitempty"`
	Age            string        `json:"age,omitempty"`
	Content        string        `json:"content,omitempty"`
}

func memorySuggestionText(action string, p MemoryPayload) string {
	switch action {
	case MemoryActArchive:
		return "建议归档"
	case MemoryActMerge:
		return "建议合并到 " + p.Into
	case MemoryActKind:
		return "建议改为 " + p.Kind
	case MemoryActSummary:
		return "建议补摘要：" + p.Summary
	case MemoryActWhen:
		return "建议补触发词：" + strings.Join(p.Keywords, ", ")
	}
	return action
}

func toMemorySuggestion(r jobstore.MemorySuggestion, projectKey string) MemorySuggestion {
	var p MemoryPayload
	_ = json.Unmarshal([]byte(r.PayloadJSON), &p)
	return MemorySuggestion{ID: r.ID, TrackerID: r.TrackerID, ProjectKey: projectKey, Key: r.MemoryKey, Action: r.Action, Payload: p,
		Text: memorySuggestionText(r.Action, p), Reason: r.Reason, State: r.State, By: r.By, CreatedAt: r.CreatedAt,
		DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy, Note: r.Note}
}

// liveMemory reads one live mirrored memory: its record and decoded body.
func (s *Service) liveMemory(trackerID, key string) (jobstore.TrackerRecord, tracker.Memory, error) {
	rec, ok, err := s.d.Store.GetTrackerMemory(trackerID, key)
	if err != nil {
		return jobstore.TrackerRecord{}, tracker.Memory{}, err
	}
	if !ok || rec.Deleted {
		return jobstore.TrackerRecord{}, tracker.Memory{}, fmt.Errorf("%w: %s/%s", ErrMemoryNotFound, trackerID, key)
	}
	var m tracker.Memory
	_ = json.Unmarshal(rec.Body, &m)
	if m.Key == "" {
		m.Key = key
	}
	return rec, m, nil
}

func (s *Service) trackerProject(trackerID string) string {
	repos, err := s.d.Store.ListTrackerRepos()
	if err != nil {
		return ""
	}
	for _, r := range repos {
		if r.TrackerID == trackerID {
			return r.ProjectKey
		}
	}
	return ""
}

// normalizeMemoryPayload validates the payload of action against the current memory and
// keeps only the action's own fields.
func (s *Service) normalizeMemoryPayload(in MemorySuggestInput, cur tracker.Memory) (MemoryPayload, error) {
	p := in.Payload
	bad := func(format string, args ...any) (MemoryPayload, error) {
		return MemoryPayload{}, fmt.Errorf("%w: "+format, append([]any{ErrInvalidMemorySuggestion}, args...)...)
	}
	switch in.Action {
	case MemoryActArchive:
		return MemoryPayload{}, nil
	case MemoryActMerge:
		into := strings.TrimSpace(p.Into)
		if into == "" || into == in.Key {
			return bad("merge needs payload.into: another memory key")
		}
		if _, _, err := s.liveMemory(in.TrackerID, into); err != nil {
			return MemoryPayload{}, err
		}
		content := strings.TrimSpace(p.Content)
		if utf8.RuneCountInString(content) > memoryMergeContentRunes {
			return bad("payload.content is longer than %d characters", memoryMergeContentRunes)
		}
		return MemoryPayload{Into: into, Content: content}, nil
	case MemoryActKind:
		kind := strings.TrimSpace(p.Kind)
		if !tracker.ValidMemoryKind(kind) {
			return bad("payload.kind must be rule|note|handoff")
		}
		if kind == cur.EffectiveKind() {
			return bad("memory %s is already %s", in.Key, kind)
		}
		return MemoryPayload{Kind: kind}, nil
	case MemoryActSummary:
		sum := strings.Join(strings.Fields(p.Summary), " ")
		if sum == "" {
			return bad("payload.summary is required")
		}
		if n := utf8.RuneCountInString(sum); n > tracker.MemorySummaryMaxRunes {
			return bad("payload.summary has %d characters, at most %d", n, tracker.MemorySummaryMaxRunes)
		}
		if sum == strings.TrimSpace(cur.Summary) {
			return bad("memory %s already has this summary", in.Key)
		}
		return MemoryPayload{Summary: sum}, nil
	case MemoryActWhen:
		have := map[string]bool{}
		if cur.When != nil {
			for _, k := range cur.When.Keywords {
				have[strings.ToLower(k)] = true
			}
		}
		var kws []string
		seen := map[string]bool{}
		fresh := false
		for _, k := range p.Keywords {
			k = strings.TrimSpace(k)
			lk := strings.ToLower(k)
			if k == "" || seen[lk] {
				continue
			}
			if utf8.RuneCountInString(k) > memoryKeywordRunes {
				return bad("keyword %q is longer than %d characters", k, memoryKeywordRunes)
			}
			seen[lk] = true
			fresh = fresh || !have[lk]
			kws = append(kws, k)
		}
		if len(kws) == 0 || len(kws) > memoryWhenMaxKeywords {
			return bad("payload.keywords needs 1..%d keywords", memoryWhenMaxKeywords)
		}
		if !fresh {
			return bad("memory %s already has these keywords", in.Key)
		}
		return MemoryPayload{Keywords: kws}, nil
	}
	return bad("action must be archive|merge|kind|summary|when")
}

// SuggestMemory validates and records one memory suggestion. recorded is false when the
// same key + action is already pending (that suggestion is returned).
func (s *Service) SuggestMemory(in MemorySuggestInput, by, jobID string) (MemorySuggestion, bool, error) {
	in.TrackerID, in.Key, in.Action = strings.TrimSpace(in.TrackerID), strings.TrimSpace(in.Key), strings.TrimSpace(in.Action)
	in.Reason = strings.Join(strings.Fields(in.Reason), " ")
	switch {
	case in.TrackerID == "" || in.Key == "":
		return MemorySuggestion{}, false, fmt.Errorf("%w: tracker_id and key are required", ErrInvalidMemorySuggestion)
	case !ValidMemoryAction(in.Action):
		return MemorySuggestion{}, false, fmt.Errorf("%w: action must be archive|merge|kind|summary|when", ErrInvalidMemorySuggestion)
	case in.Reason == "":
		return MemorySuggestion{}, false, fmt.Errorf("%w: reason is required (one line, at most %d characters)", ErrInvalidMemorySuggestion, memoryReasonRunes)
	case utf8.RuneCountInString(in.Reason) > memoryReasonRunes:
		return MemorySuggestion{}, false, fmt.Errorf("%w: reason has %d characters, at most %d", ErrInvalidMemorySuggestion, utf8.RuneCountInString(in.Reason), memoryReasonRunes)
	}
	rec, cur, err := s.liveMemory(in.TrackerID, in.Key)
	if err != nil {
		return MemorySuggestion{}, false, err
	}
	payload, err := s.normalizeMemoryPayload(in, cur)
	if err != nil {
		return MemorySuggestion{}, false, err
	}
	var targetRev int64
	if in.Action == MemoryActMerge {
		trec, _, err := s.liveMemory(in.TrackerID, payload.Into)
		if err != nil {
			return MemorySuggestion{}, false, err
		}
		targetRev = trec.Rev
	}
	st := s.d.Store
	now := s.d.Now()
	project := s.trackerProject(in.TrackerID)
	pending, err := st.ListMemorySuggestions(jobstore.MemorySuggestPending)
	if err != nil {
		return MemorySuggestion{}, false, err
	}
	for _, p := range pending {
		if p.TrackerID == in.TrackerID && p.MemoryKey == in.Key && p.Action == in.Action {
			return toMemorySuggestion(p, project), false, nil
		}
	}
	dismissed, err := st.MemorySuggestionDismissedSince(in.TrackerID, in.Key, in.Action, now.Add(-MemorySuggestCooldown).Unix())
	if err != nil {
		return MemorySuggestion{}, false, err
	}
	if dismissed {
		return MemorySuggestion{}, false, fmt.Errorf("%w: %s %s was dismissed within %d days; do not propose it again", ErrMemorySuggestCooldown,
			in.Action, in.Key, int(MemorySuggestCooldown.Hours()/24))
	}
	used, err := st.CountMemorySuggestionsSince(startOfDay(now).Unix())
	if err != nil {
		return MemorySuggestion{}, false, err
	}
	if used >= MemorySuggestDailyCap {
		return MemorySuggestion{}, false, fmt.Errorf("%w: %d today already (at most %d per day)", ErrMemorySuggestCap, used, MemorySuggestDailyCap)
	}
	raw, _ := json.Marshal(payload)
	row, recorded, err := st.AddMemorySuggestion(jobstore.MemorySuggestion{TrackerID: in.TrackerID, MemoryKey: in.Key, Action: in.Action,
		PayloadJSON: string(raw), Reason: in.Reason, By: by, JobID: jobID, BaseRev: rec.Rev, TargetRev: targetRev, CreatedAt: now.Unix()})
	if err != nil {
		return MemorySuggestion{}, false, err
	}
	return toMemorySuggestion(row, project), recorded, nil
}

// ListMemorySuggestions lists suggestions in one state ("" = pending).
func (s *Service) ListMemorySuggestions(state string) ([]MemorySuggestion, error) {
	if state == "" {
		state = jobstore.MemorySuggestPending
	}
	if state == "all" {
		state = ""
	}
	rows, err := s.d.Store.ListMemorySuggestions(state)
	if err != nil {
		return nil, err
	}
	projects := map[string]string{}
	if repos, err := s.d.Store.ListTrackerRepos(); err == nil {
		for _, r := range repos {
			projects[r.TrackerID] = r.ProjectKey
		}
	}
	out := make([]MemorySuggestion, 0, len(rows))
	for _, r := range rows {
		out = append(out, toMemorySuggestion(r, projects[r.TrackerID]))
	}
	return out, nil
}

// pendingMemorySuggestion reads a suggestion that must still be pending.
func (s *Service) pendingMemorySuggestion(id int64) (jobstore.MemorySuggestion, error) {
	row, err := s.d.Store.GetMemorySuggestion(id)
	if err != nil {
		return jobstore.MemorySuggestion{}, err
	}
	if row.State != jobstore.MemorySuggestPending {
		return jobstore.MemorySuggestion{}, fmt.Errorf("%w: suggestion %d is %s", jobstore.ErrMemorySuggestionDecided, id, row.State)
	}
	return row, nil
}

// DismissMemorySuggestion records 「忽略」: the same key + action is not proposed again for
// MemorySuggestCooldown.
func (s *Service) DismissMemorySuggestion(id int64, by string) (MemorySuggestion, error) {
	if _, err := s.pendingMemorySuggestion(id); err != nil {
		return MemorySuggestion{}, err
	}
	row, err := s.d.Store.DecideMemorySuggestion(id, jobstore.MemorySuggestDismissed, by, "")
	if err != nil {
		return MemorySuggestion{}, err
	}
	return toMemorySuggestion(row, s.trackerProject(row.TrackerID)), nil
}

// AdoptMemorySuggestion applies a pending suggestion to the server copy of the memory and
// marks it adopted. A memory (or merge target) that is gone marks the suggestion stale and
// returns ErrMemoryNotFound; a memory changed since the suggestion (rev moved, or a
// concurrent write won the compare-and-set) marks it stale and returns
// ErrMemorySuggestionStale. Other errors leave it pending; adopting again is idempotent.
func (s *Service) AdoptMemorySuggestion(id int64, by string) (MemorySuggestion, error) {
	row, err := s.pendingMemorySuggestion(id)
	if err != nil {
		return MemorySuggestion{}, err
	}
	var p MemoryPayload
	_ = json.Unmarshal([]byte(row.PayloadJSON), &p)
	if err := s.applyMemorySuggestion(row, p, by); err != nil {
		if errors.Is(err, jobstore.ErrTrackerConflict) && !errors.Is(err, ErrMemorySuggestionStale) {
			err = fmt.Errorf("%w (%s %s: %v)", ErrMemorySuggestionStale, row.Action, row.MemoryKey, err)
		}
		if errors.Is(err, ErrMemoryNotFound) || errors.Is(err, ErrMemorySuggestionStale) {
			_, _ = s.d.Store.DecideMemorySuggestion(id, jobstore.MemorySuggestStale, by, err.Error())
		}
		return MemorySuggestion{}, err
	}
	out, err := s.d.Store.DecideMemorySuggestion(id, jobstore.MemorySuggestAdopted, by, memorySuggestionText(row.Action, p))
	if err != nil {
		return MemorySuggestion{}, err
	}
	return toMemorySuggestion(out, s.trackerProject(out.TrackerID)), nil
}

// memoryTombstone is the body of the tombstone an adoption writes. SuggestionID marks it
// as this suggestion's own (a retried adoption recognises its earlier write).
type memoryTombstone struct {
	ArchiveReason string `json:"archive_reason"`
	MergedInto    string `json:"merged_into,omitempty"`
	SuggestionID  int64  `json:"suggestion_id"`
}

func staleRev(what, key string, want, got int64) error {
	return fmt.Errorf("%w (%s %s: rev %d at suggestion time, now %d)", ErrMemorySuggestionStale, what, key, want, got)
}

func (s *Service) applyMemorySuggestion(row jobstore.MemorySuggestion, p MemoryPayload, by string) error {
	st := s.d.Store
	srcRec, ok, err := st.GetTrackerMemory(row.TrackerID, row.MemoryKey)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s/%s", ErrMemoryNotFound, row.TrackerID, row.MemoryKey)
	}
	if srcRec.Deleted {
		// Our own tombstone from an earlier attempt (the decision write failed after it):
		// the change is already applied.
		var tb memoryTombstone
		if json.Unmarshal(srcRec.Body, &tb) == nil && tb.SuggestionID == row.ID && tracker.IsArchiveTombstone(srcRec.DeletedBy) {
			return nil
		}
		return fmt.Errorf("%w: %s/%s", ErrMemoryNotFound, row.TrackerID, row.MemoryKey)
	}
	if srcRec.Rev != row.BaseRev {
		return staleRev("memory", row.MemoryKey, row.BaseRev, srcRec.Rev)
	}
	var cur tracker.Memory
	_ = json.Unmarshal(srcRec.Body, &cur)
	now := s.d.Now().UTC().Format(time.RFC3339Nano)
	patch := func(trackerID, key string, rev int64, fields map[string]any) error {
		raw := make(map[string]json.RawMessage, len(fields))
		for k, v := range fields {
			b, err := json.Marshal(v)
			if err != nil {
				return err
			}
			raw[k] = b
		}
		_, err := st.PatchTrackerMemory(trackerID, key, rev, raw, now, by)
		return err
	}
	// Both tombstones are archive tombstones: clones keep the text in their archive file.
	tombstone := func(tb memoryTombstone) error {
		tb.SuggestionID = row.ID
		b, _ := json.Marshal(tb)
		_, err := st.TombstoneTrackerMemoryAt(row.TrackerID, row.MemoryKey, row.BaseRev, b, now, tracker.ArchiveTombstonePrefix+by)
		return err
	}
	reason := "管家建议（今天卡采纳）：" + row.Reason
	switch row.Action {
	case MemoryActArchive:
		return tombstone(memoryTombstone{ArchiveReason: reason})
	case MemoryActMerge:
		trec, target, err := s.liveMemory(row.TrackerID, p.Into)
		if err != nil {
			return err
		}
		merged := strings.TrimSpace(p.Content)
		if merged == "" {
			merged = strings.TrimSpace(cur.Content)
		}
		// Idempotent: a target that already holds the merged text (an earlier attempt
		// patched it, then failed before the tombstone) is not appended to again.
		if merged == "" || !strings.Contains(target.Content, merged) {
			if row.TargetRev == 0 || trec.Rev != row.TargetRev {
				return staleRev("merge target", p.Into, row.TargetRev, trec.Rev)
			}
			content := p.Content
			if content == "" {
				content = strings.TrimRight(target.Content, "\n") + "\n\n" + strings.TrimSpace(cur.Content)
			}
			if err := patch(row.TrackerID, p.Into, trec.Rev, map[string]any{"content": content}); err != nil {
				return err
			}
		}
		return tombstone(memoryTombstone{ArchiveReason: "已合并到 " + p.Into + "；" + reason, MergedInto: p.Into})
	case MemoryActKind:
		fields := map[string]any{"kind": p.Kind}
		if p.Kind != tracker.MemoryKindHandoff && cur.ExpiresAt != "" {
			fields["expires_at"] = "" // a TTL only applies to handoff
		}
		return patch(row.TrackerID, row.MemoryKey, row.BaseRev, fields)
	case MemoryActSummary:
		return patch(row.TrackerID, row.MemoryKey, row.BaseRev, map[string]any{"summary": p.Summary})
	case MemoryActWhen:
		when := tracker.MemoryWhen{}
		if cur.When != nil {
			when = *cur.When
		}
		have := map[string]bool{}
		for _, k := range when.Keywords {
			have[strings.ToLower(k)] = true
		}
		for _, k := range p.Keywords {
			if !have[strings.ToLower(k)] {
				when.Keywords = append(when.Keywords, k)
			}
		}
		return patch(row.TrackerID, row.MemoryKey, row.BaseRev, map[string]any{"when": when})
	}
	return fmt.Errorf("%w: unknown action %q", ErrInvalidMemorySuggestion, row.Action)
}

// ---------------------------------------------------------------- cards

// memoryCards adds one card per pending suggestion whose memory is still live.
func (b *builder) memoryCards() error {
	rows, err := b.store().ListMemorySuggestions(jobstore.MemorySuggestPending)
	if err != nil || len(rows) == 0 {
		return err
	}
	projects := map[string]string{}
	repos, err := b.store().ListTrackerRepos()
	if err != nil {
		return err
	}
	for _, r := range repos {
		projects[r.TrackerID] = r.ProjectKey
	}
	now := b.s.d.Now()
	for _, r := range rows {
		_, cur, err := b.s.liveMemory(r.TrackerID, r.MemoryKey)
		if errors.Is(err, ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		sg := toMemorySuggestion(r, projects[r.TrackerID])
		if sg.Action == MemoryActMerge {
			if _, _, err := b.s.liveMemory(r.TrackerID, sg.Payload.Into); err != nil {
				if errors.Is(err, ErrMemoryNotFound) {
					continue
				}
				return err
			}
		}
		b.cards = append(b.cards, Card{
			Key: KindMemory + ":" + strconv.FormatInt(r.ID, 10), Kind: KindMemory, Tag: "记忆整理",
			Title:      oneLine(memoryActLabels[sg.Action]+"：记忆 "+sg.Key, titleRunes),
			ProjectKey: sg.ProjectKey, WaitingSince: r.CreatedAt, ActivityAt: r.CreatedAt,
			Summary: oneLine(sg.Text+" · "+sg.Reason, summaryRunes),
			Refs:    Refs{MemorySuggestionID: r.ID, TrackerID: r.TrackerID, MemoryKey: r.MemoryKey},
			Memory: &MemoryCard{TrackerID: r.TrackerID, Key: r.MemoryKey, Action: sg.Action, Payload: sg.Payload,
				CurrentKind: cur.EffectiveKind(), CurrentSummary: tracker.DisplayMemorySummary(cur.MemoryMeta, cur.Content),
				Age: tracker.AgeText(cur.UpdatedAt, now), Content: capRunes(strings.TrimSpace(cur.Content), memoryCardContentRunes)},
			Actions: []Action{{ID: "adopt", Label: "采纳", Style: "ok"}, {ID: "dismiss", Label: "忽略"}},
		})
	}
	return nil
}

// memoryCardAlive is cardAlive for a memory card.
func (s *Service) memoryCardAlive(ref string) (bool, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return false, nil
	}
	row, err := s.d.Store.GetMemorySuggestion(id)
	if errors.Is(err, jobstore.ErrMemorySuggestionNotFound) {
		return false, nil
	}
	if err != nil || row.State != jobstore.MemorySuggestPending {
		return false, err
	}
	if _, _, err := s.liveMemory(row.TrackerID, row.MemoryKey); err != nil {
		if errors.Is(err, ErrMemoryNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
