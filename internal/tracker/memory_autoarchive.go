package tracker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Automatic archiving of memories that are settled beyond doubt (attention-surfaces
// review design §8, layer 1). It is deterministic and reads evidence only (no model):
//
//   - a handoff that expired more than AutoArchiveHandoffGrace ago;
//   - a note (kind=note) that references at least one repository path, with every
//     referenced path AND every referenced commit gone. A note naming only commits is left
//     alone (a shallow clone also reports old commits as missing), and a store without a
//     checkout (the global memories) has no path check, so its notes are never touched.
//
// kind=rule is never archived. A memory changed within the grace period is left alone,
// which also gives a just-restored memory a week before it can be archived again. A
// memory that silences the matching finding (doctor_ignore, prime.doctor.suppress) stays.
// The archive entry (memories-archive.jsonl, restorable with `memory restore`) is the
// audit record: archived_by is AutoArchiveActor and archive_reason says why.
const (
	// AutoArchiveHandoffGrace is how long an expired handoff is kept before it is archived.
	AutoArchiveHandoffGrace = 7 * 24 * time.Hour
	// AutoArchiveActor is the archived_by of an automatic archive.
	AutoArchiveActor = "auto-archive"
	// autoArchiveListWindow bounds how long `memory doctor` lists an automatic archive.
	autoArchiveListWindow = 30 * 24 * time.Hour
)

// AutoArchived is one memory moved out by AutoArchiveMemories.
type AutoArchived struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	ArchivedAt string `json:"archived_at,omitempty"`
}

// AutoArchiveEnabled reports prime.doctor.auto_archive (default true).
func (c PrimeConfig) AutoArchiveEnabled() bool {
	return c.Doctor == nil || c.Doctor.AutoArchive == nil || *c.Doctor.AutoArchive
}

// autoArchiveReason returns why m is settled beyond doubt, or "" when it is not.
func autoArchiveReason(m Memory, opts DoctorOptions, missingCommits map[string]bool, suppress []string) string {
	silenced := func(slug string) bool { return hasTag(suppress, slug) || hasTag(m.DoctorIgnore, slug) }
	now := opts.Now
	if t, ok := parseTime(m.UpdatedAt); !ok || now.Sub(t) < AutoArchiveHandoffGrace {
		return "" // touched within the grace period (or undated): not settled
	}
	switch m.EffectiveKind() {
	case MemoryKindHandoff:
		at, ok := MemoryExpiresAt(m.MemoryMeta, m.Tags, m.UpdatedAt)
		if !ok || silenced(DoctorHandoffExpired) || now.Sub(at) <= AutoArchiveHandoffGrace {
			return ""
		}
		return "auto-archived: handoff expired " + at.Format("2006-01-02") + " (more than 7 days ago)"
	case MemoryKindNote:
		if len(opts.Roots) == 0 || silenced(DoctorPathMissing) || silenced(DoctorCommitMissing) {
			return ""
		}
		paths := memoryPathRefs(m.Content)
		if len(paths) == 0 {
			return ""
		}
		for _, p := range paths {
			if pathExistsUnder(p, opts.Roots) {
				return ""
			}
		}
		commits := memoryCommitRefs(m.Content)
		for _, h := range commits {
			if !missingCommits[h] { // present, or git could not tell (nil map)
				return ""
			}
		}
		refs := append(append([]string(nil), paths...), commits...)
		return "auto-archived: every referenced path and commit is gone (" + detailList(refs) + ")"
	}
	return ""
}

// AutoArchiveCandidates lists the memories autoArchiveReason accepts, by key.
func AutoArchiveCandidates(items []Memory, opts DoctorOptions, suppress []string) map[string]string {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var missingCommits map[string]bool
	if opts.CommitsMissing != nil {
		var hashes []string
		seen := map[string]bool{}
		for _, m := range items {
			if m.EffectiveKind() != MemoryKindNote {
				continue
			}
			for _, h := range memoryCommitRefs(m.Content) {
				if !seen[h] {
					seen[h] = true
					hashes = append(hashes, h)
				}
			}
		}
		if len(hashes) > 0 {
			missingCommits = opts.CommitsMissing(hashes)
		}
	}
	out := map[string]string{}
	for _, m := range items {
		if reason := autoArchiveReason(m, opts, missingCommits, suppress); reason != "" {
			out[m.Key] = reason
		}
	}
	return out
}

// AutoArchiveMemories archives the settled memories of this store (see the file comment)
// and returns them, sorted by key. It does nothing when prime.doctor.auto_archive is off,
// and is idempotent: an archived memory is no longer live. The decision is made from a
// snapshot, then re-checked under the lock (the memory must still be live and unchanged).
func (s *Store) AutoArchiveMemories(now time.Time) ([]AutoArchived, error) {
	if now.IsZero() {
		now = time.Now()
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return nil, err
	}
	if !cfg.Prime.AutoArchiveEnabled() {
		return nil, nil
	}
	items, err := s.ReadMemories()
	if err != nil {
		return nil, err
	}
	opts := DoctorOptions{Now: now}
	if root := s.RepoRoot(); root != "" {
		opts.Roots = doctorRoots(root)
		opts.CommitsMissing = func(hashes []string) map[string]bool { return gitCommitsMissing(root, hashes) }
	}
	reasons := AutoArchiveCandidates(items, opts, cfg.Prime.DoctorSuppress())
	if len(reasons) == 0 {
		return nil, nil
	}
	snapshot := map[string]Memory{}
	for _, m := range items {
		snapshot[m.Key] = m
	}
	var out []AutoArchived
	err = s.updateMemoriesAndArchive(func(live []Memory, archived []ArchivedMemory) ([]Memory, []ArchivedMemory, error) {
		stamp := now.UTC().Format(time.RFC3339Nano)
		keptLive := live[:0]
		for _, m := range live {
			reason, ok := reasons[m.Key]
			if !ok || m.UpdatedAt != snapshot[m.Key].UpdatedAt {
				keptLive = append(keptLive, m)
				continue
			}
			kept := archived[:0]
			for _, a := range archived {
				if a.Key != m.Key {
					kept = append(kept, a)
				}
			}
			archived = append(kept, ArchivedMemory{Memory: m, ArchivedAt: stamp, ArchivedBy: AutoArchiveActor, ArchiveReason: reason})
			out = append(out, AutoArchived{Key: m.Key, Kind: m.EffectiveKind(), Reason: reason, ArchivedAt: stamp})
		}
		return keptLive, archived, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// recentAutoArchived lists the automatic archives of the last autoArchiveListWindow still
// sitting in the archive file (a restored memory is no longer there).
func (s *Store) recentAutoArchived(now time.Time) []AutoArchived {
	archived, err := s.ReadArchivedMemories()
	if err != nil {
		return nil
	}
	var out []AutoArchived
	for _, a := range archived {
		if a.ArchivedBy != AutoArchiveActor {
			continue
		}
		if t, ok := parseTime(a.ArchivedAt); ok && now.Sub(t) > autoArchiveListWindow {
			continue
		}
		out = append(out, AutoArchived{Key: a.Key, Kind: a.EffectiveKind(), Reason: a.ArchiveReason, ArchivedAt: a.ArchivedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func formatAutoArchived(items []AutoArchived) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "auto-archived (settled beyond doubt; `gofer memory restore <key>` brings one back): %d\n", len(items))
	for _, it := range items {
		fmt.Fprintf(&b, "  - %s [%s]: %s\n", it.Key, it.Kind, it.Reason)
	}
	return b.String()
}
