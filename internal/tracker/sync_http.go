package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/util"
)

// syncWireRecord is one mirrored record as the server returns it.
type syncWireRecord struct {
	ID        string          `json:"id"`
	Body      json.RawMessage `json:"body"`
	Rev       int64           `json:"rev"`
	UpdatedAt string          `json:"updated_at"`
	Deleted   bool            `json:"deleted"`
	DeletedAt string          `json:"deleted_at"`
	DeletedBy string          `json:"deleted_by"`
}

type syncHTTPResponse struct {
	IssueCursor  int64            `json:"issue_cursor"`
	MemoryCursor int64            `json:"memory_cursor"`
	Issues       []syncWireRecord `json:"issues"`
	Memories     []syncWireRecord `json:"memories"`
	// Accepted holds the new server rev of every pushed record that was written.
	Accepted struct {
		Issues   map[string]int64 `json:"issues"`
		Memories map[string]int64 `json:"memories"`
	} `json:"accepted"`
	// Conflicts holds the current server record for every pushed record whose base
	// rev was stale (not written); the client merges and pushes again.
	Conflicts struct {
		Issues   []syncWireRecord `json:"issues"`
		Memories []syncWireRecord `json:"memories"`
	} `json:"conflicts"`
}

// syncPush is one record pushed to the server. Rev is the server rev the client
// last saw for it (0 = never seen), not a new rev.
type syncPush struct {
	ID        string          `json:"id"`
	Body      json.RawMessage `json:"body"`
	Rev       int64           `json:"rev"`
	UpdatedAt string          `json:"updated_at"`
	Deleted   bool            `json:"deleted,omitempty"`
	DeletedAt string          `json:"deleted_at,omitempty"`
	DeletedBy string          `json:"deleted_by,omitempty"`
}

type syncMeta struct {
	LastSyncAt   string `json:"last_sync_at"`
	Summary      string `json:"summary"`
	IssueCursor  int64  `json:"issue_cursor"`
	MemoryCursor int64  `json:"memory_cursor"`
}

// syncRevs is .local/sync-revs.json: the server rev last seen for every record
// (issues by id, memories by key, tombstones included). Pushes send it as the
// base rev so the server can tell a stale write from a fresh one.
type syncRevs struct {
	Issues   map[string]int64 `json:"issues"`
	Memories map[string]int64 `json:"memories"`
}

const (
	syncRevsFile = "sync-revs.json"
	// maxSyncRounds bounds push → conflict → merge → push within one sync.
	maxSyncRounds = 3
)

type syncClient struct {
	ctx      context.Context
	endpoint string
	token    string
	payload  map[string]any
}

func (c syncClient) post(issues, memories []syncPush, issueSince, memorySince int64) (syncHTTPResponse, error) {
	payload := make(map[string]any, util.CapSum(len(c.payload), 4))
	for k, v := range c.payload {
		payload[k] = v
	}
	if issues == nil {
		issues = []syncPush{}
	}
	if memories == nil {
		memories = []syncPush{}
	}
	payload["issues"], payload["memories"], payload["issue_since"], payload["memory_since"] = issues, memories, issueSince, memorySince
	body, err := json.Marshal(payload)
	if err != nil {
		return syncHTTPResponse{}, err
	}
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.endpoint+"/v1/tracker/sync", bytes.NewReader(body))
	if err != nil {
		return syncHTTPResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return syncHTTPResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return syncHTTPResponse{}, fmt.Errorf("sync server %s: %s", resp.Status, string(b))
	}
	var wire syncHTTPResponse
	err = json.NewDecoder(resp.Body).Decode(&wire)
	return wire, err
}

// SyncHTTP is the command/runtime sync path. It persists the last successful
// base under tracker/.local and never changes the repository files when the
// first request fails.
func SyncHTTP(ctx context.Context, s *Store, endpoint string) (SyncReport, error) {
	return SyncHTTPWithToken(ctx, s, endpoint, "")
}

// SyncHTTPWithToken pushes local changes (each with the server rev last seen),
// pulls the server changes since the cursor, three-way merges them and, when the
// server answered a push with a conflict, pushes the merged result again (at most
// maxSyncRounds requests). The first sync without .local/sync-revs.json does a full
// pull and repairs any divergence record by record (newer body timestamp wins).
func SyncHTTPWithToken(ctx context.Context, s *Store, endpoint, token string) (SyncReport, error) {
	if s == nil || endpoint == "" {
		return SyncReport{}, fmt.Errorf("tracker store and endpoint are required")
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return SyncReport{}, err
	}
	cfg = migrateLegacyTrackerID(ctx, s, cfg, endpoint, token)
	issues, err := s.ReadIssues()
	if err != nil {
		return SyncReport{}, err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return SyncReport{}, err
	}
	base, baseErr := readSyncBase(s.Dir)
	meta := readSyncMeta(s.Dir)
	revs, haveRevs, err := readSyncRevs(s.Dir)
	if err != nil {
		return SyncReport{}, err
	}
	local := SyncSnapshot{Issues: issues, Memories: memories}
	root := filepath.Dir(filepath.Dir(s.Dir))
	client := syncClient{ctx: ctx, endpoint: endpoint, token: token, payload: map[string]any{"tracker_id": cfg.TrackerID, "project_key": cfg.ProjectKey, "prefix": cfg.Prefix, "rel_path": filepath.ToSlash(root)}}
	issueSince, memorySince := meta.IssueCursor, meta.MemoryCursor
	var report SyncReport
	if !haveRevs {
		// One-time repair (first sync with rev tracking): pull everything, then decide
		// every diverged record by its own timestamps before pushing.
		wire, err := client.post(nil, nil, 0, 0)
		if err != nil {
			return report, err
		}
		view := newServerView(SyncSnapshot{})
		view.applyWire(wire.Issues, wire.Memories, &revs)
		var oldBase *SyncSnapshot
		if baseErr == nil {
			oldBase = &base
		}
		var toServer, toLocal int
		local, toServer, toLocal = repairLocal(local, view, oldBase)
		if oldBase != nil {
			report.RepairedToServer, report.RepairedToLocal = toServer, toLocal
		}
		base = view.snapshot()
		issueSince, memorySince = wire.IssueCursor, wire.MemoryCursor
	}
	var roundErr error
	for round := 1; ; round++ {
		pushI, pushM := syncPushes(base, local, revs)
		if round > 1 && len(pushI) == 0 && len(pushM) == 0 {
			break
		}
		if round > maxSyncRounds {
			report.Unresolved = pushKeys(pushI, pushM)
			break
		}
		wire, err := client.post(pushI, pushM, issueSince, memorySince)
		if err != nil {
			if round == 1 {
				return report, err
			}
			// Keep the merged state of the earlier rounds; the next sync pushes the rest.
			report.Unresolved, roundErr = pushKeys(pushI, pushM), err
			break
		}
		issueSince, memorySince = wire.IssueCursor, wire.MemoryCursor
		view := newServerView(base)
		view.applyAccepted(pushI, pushM, wire, local, &revs)
		remoteMemories := view.applyWire(wire.Issues, wire.Memories, &revs)
		remoteMemories = append(remoteMemories, view.applyWire(wire.Conflicts.Issues, wire.Conflicts.Memories, &revs)...)
		report.Rejected += len(wire.Conflicts.Issues) + len(wire.Conflicts.Memories)
		remote := view.snapshot()
		merged, rep := mergeSyncRound(base, local, remote, remoteMemories)
		report.Conflicts = append(report.Conflicts, rep.Conflicts...)
		base, local = remote, merged
	}
	report.Summary = report.summary()
	if err := s.WriteIssues(local.Issues); err != nil {
		return report, err
	}
	if err := s.UpdateMemories(func([]Memory) ([]Memory, error) { return local.Memories, nil }); err != nil {
		return report, err
	}
	if err := writeSyncBase(s.Dir, base); err != nil {
		return report, err
	}
	if err := writeSyncRevs(s.Dir, revs); err != nil {
		return report, err
	}
	metaOut, _ := json.Marshal(syncMeta{LastSyncAt: Now(), Summary: report.Summary, IssueCursor: issueSince, MemoryCursor: memorySince})
	_ = atomicWrite(filepath.Join(s.Dir, ".local", "sync-status.json"), append(metaOut, '\n'))
	if roundErr != nil {
		return report, fmt.Errorf("sync incomplete (merged state saved, rerun to push %d record(s)): %w", len(report.Unresolved), roundErr)
	}
	return report, nil
}

// mergeSyncRound three-way merges one round and applies the memory tombstone policy.
func mergeSyncRound(base, local, remote SyncSnapshot, remoteMemories []ServerMemory) (SyncSnapshot, SyncReport) {
	merged, report := ThreeWayMerge(base, local, remote)
	serverMemBase := make([]ServerMemory, 0, len(base.Memories))
	for _, m := range base.Memories {
		serverMemBase = append(serverMemBase, ServerMemory{Memory: m})
	}
	serverMemLocal := make([]ServerMemory, 0, len(local.Memories))
	for _, m := range local.Memories {
		serverMemLocal = append(serverMemLocal, ServerMemory{Memory: m})
	}
	serverMemMerged, memReport := MergeServerMemories(serverMemBase, serverMemLocal, remoteMemories)
	report.Conflicts = append(report.Conflicts, memReport.Conflicts...)
	deleted := map[string]bool{}
	for _, item := range serverMemMerged {
		if item.Deleted {
			deleted[item.Key] = true
		}
	}
	if len(deleted) > 0 {
		kept := merged.Memories[:0]
		for _, m := range merged.Memories {
			if !deleted[m.Key] {
				kept = append(kept, m)
			}
		}
		merged.Memories = kept
	}
	return merged, report
}

// syncPushes lists the records of local that differ from base (the last known
// server state): changed or new issues/memories, and tombstones for memories
// removed locally. Each carries the known server rev.
func syncPushes(base, local SyncSnapshot, revs syncRevs) ([]syncPush, []syncPush) {
	baseIssues := indexIssues(base.Issues)
	issues := make([]syncPush, 0, len(local.Issues))
	for _, issue := range local.Issues {
		if old, ok := baseIssues[issue.ID]; ok && bytes.Equal(mustJSON(old), mustJSON(issue)) {
			continue
		}
		issues = append(issues, syncPush{ID: issue.ID, Body: mustJSON(issue), Rev: revs.Issues[issue.ID], UpdatedAt: issue.UpdatedAt})
	}
	baseMem := indexMemories(base.Memories)
	memories := make([]syncPush, 0, len(local.Memories))
	for _, memory := range local.Memories {
		if old, ok := baseMem[memory.Key]; ok && bytes.Equal(mustJSON(old), mustJSON(memory)) {
			continue
		}
		memories = append(memories, syncPush{ID: memory.Key, Body: mustJSON(memory), Rev: revs.Memories[memory.Key], UpdatedAt: memory.UpdatedAt})
	}
	localMem := indexMemories(local.Memories)
	for _, m := range base.Memories {
		if _, ok := localMem[m.Key]; !ok {
			now := Now()
			memories = append(memories, syncPush{ID: m.Key, Body: json.RawMessage("{}"), Rev: revs.Memories[m.Key], UpdatedAt: now, Deleted: true, DeletedAt: now, DeletedBy: "local"})
		}
	}
	return issues, memories
}

func pushKeys(issues, memories []syncPush) []string {
	out := make([]string, 0, util.CapSum(len(issues), len(memories)))
	for _, p := range issues {
		out = append(out, "issue:"+p.ID)
	}
	for _, p := range memories {
		out = append(out, "memory:"+p.ID)
	}
	return out
}

// serverView is the client's picture of the server's live records.
type serverView struct {
	issues   map[string]Issue
	memories map[string]Memory
	// tombs holds server memory tombstones seen in this view.
	tombs map[string]ServerMemory
}

func newServerView(base SyncSnapshot) *serverView {
	return &serverView{issues: indexIssues(base.Issues), memories: indexMemories(base.Memories), tombs: map[string]ServerMemory{}}
}

// applyAccepted records the pushed bodies the server wrote, with their new revs.
func (v *serverView) applyAccepted(issues, memories []syncPush, wire syncHTTPResponse, local SyncSnapshot, revs *syncRevs) {
	localIssues, localMem := indexIssues(local.Issues), indexMemories(local.Memories)
	for _, p := range issues {
		if rev, ok := wire.Accepted.Issues[p.ID]; ok {
			v.issues[p.ID] = localIssues[p.ID]
			revs.Issues[p.ID] = rev
		}
	}
	for _, p := range memories {
		rev, ok := wire.Accepted.Memories[p.ID]
		if !ok {
			continue
		}
		revs.Memories[p.ID] = rev
		if p.Deleted {
			delete(v.memories, p.ID)
			continue
		}
		v.memories[p.ID] = localMem[p.ID]
	}
}

// applyWire overlays server records and records their revs; it returns the
// memories (tombstones included) in the form MergeServerMemories expects.
func (v *serverView) applyWire(issues, memories []syncWireRecord, revs *syncRevs) []ServerMemory {
	for _, item := range issues {
		var issue Issue
		if err := json.Unmarshal(item.Body, &issue); err != nil {
			continue
		}
		if issue.ID == "" {
			issue.ID = item.ID
		}
		v.issues[item.ID] = issue
		revs.Issues[item.ID] = item.Rev
	}
	out := make([]ServerMemory, 0, len(memories))
	for _, item := range memories {
		var memory Memory
		_ = json.Unmarshal(item.Body, &memory)
		if memory.Key == "" {
			memory.Key = item.ID
		}
		revs.Memories[item.ID] = item.Rev
		sm := ServerMemory{Memory: memory, Deleted: item.Deleted, DeletedAt: item.DeletedAt, DeletedBy: item.DeletedBy}
		out = append(out, sm)
		if item.Deleted {
			delete(v.memories, item.ID)
			v.tombs[item.ID] = sm
			continue
		}
		delete(v.tombs, item.ID)
		v.memories[item.ID] = memory
	}
	return out
}

// snapshot returns the live records sorted by id/key (stable sync-base bytes).
func (v *serverView) snapshot() SyncSnapshot {
	out := SyncSnapshot{Issues: make([]Issue, 0, len(v.issues)), Memories: make([]Memory, 0, len(v.memories))}
	for _, i := range v.issues {
		out.Issues = append(out.Issues, i)
	}
	for _, m := range v.memories {
		out.Memories = append(out.Memories, m)
	}
	sort.Slice(out.Issues, func(i, j int) bool { return out.Issues[i].ID < out.Issues[j].ID })
	sort.Slice(out.Memories, func(i, j int) bool { return out.Memories[i].Key < out.Memories[j].Key })
	return out
}

// repairLocal settles every record whose local body differs from the server's
// (one-time, first sync with rev tracking): the newer body timestamp wins
// (issue updated_at; memory updated_at vs a tombstone's deleted_at), ties go to
// local. Server winners are applied to the returned snapshot; local winners stay
// and are pushed afterwards because they differ from the server view. A server
// memory missing locally is a local delete when the old sync base still holds
// the same body (the server never changed it since); otherwise it is pulled.
func repairLocal(local SyncSnapshot, view *serverView, oldBase *SyncSnapshot) (SyncSnapshot, int, int) {
	toServer, toLocal := 0, 0
	issues := indexIssues(local.Issues)
	for id, l := range issues {
		r, ok := view.issues[id]
		switch {
		case !ok:
			toServer++
		case bytes.Equal(mustJSON(l), mustJSON(r)):
		case laterThan(r.UpdatedAt, l.UpdatedAt):
			issues[id] = r
			toLocal++
		default:
			toServer++
		}
	}
	for id, r := range view.issues {
		if _, ok := issues[id]; !ok {
			issues[id] = r
			toLocal++
		}
	}
	memories := indexMemories(local.Memories)
	for key, l := range memories {
		if r, ok := view.memories[key]; ok {
			switch {
			case bytes.Equal(mustJSON(l), mustJSON(r)):
			case laterThan(r.UpdatedAt, l.UpdatedAt):
				memories[key] = r
				toLocal++
			default:
				toServer++
			}
			continue
		}
		if t, ok := view.tombs[key]; ok && laterThan(t.DeletedAt, l.UpdatedAt) {
			delete(memories, key)
			toLocal++
			continue
		}
		toServer++
	}
	var baseMem map[string]Memory
	if oldBase != nil {
		baseMem = indexMemories(oldBase.Memories)
	}
	for key, r := range view.memories {
		if _, ok := memories[key]; ok {
			continue
		}
		if b, ok := baseMem[key]; ok && bytes.Equal(mustJSON(b), mustJSON(r)) {
			toServer++ // deleted locally since the last sync; the tombstone is pushed
			continue
		}
		memories[key] = r
		toLocal++
	}
	out := SyncSnapshot{Issues: make([]Issue, 0, len(issues)), Memories: make([]Memory, 0, len(memories))}
	for _, i := range issues {
		out.Issues = append(out.Issues, i)
	}
	for _, m := range memories {
		out.Memories = append(out.Memories, m)
	}
	sort.Slice(out.Issues, func(i, j int) bool { return out.Issues[i].ID < out.Issues[j].ID })
	sort.Slice(out.Memories, func(i, j int) bool { return out.Memories[i].Key < out.Memories[j].Key })
	return out, toServer, toLocal
}

// laterThan reports whether timestamp a is strictly later than b. RFC 3339
// values are compared as times (RFC3339Nano trims trailing zeros, so string
// order is not reliable); anything else falls back to string order.
func laterThan(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA == nil && errB == nil {
		return ta.After(tb)
	}
	return a > b
}

func readSyncRevs(dir string) (syncRevs, bool, error) {
	revs := syncRevs{Issues: map[string]int64{}, Memories: map[string]int64{}}
	b, err := os.ReadFile(filepath.Join(dir, ".local", syncRevsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return revs, false, nil
	}
	if err != nil {
		return revs, false, err
	}
	if err := json.Unmarshal(b, &revs); err != nil {
		// A corrupt file is treated like a missing one: the full pull rebuilds it.
		return syncRevs{Issues: map[string]int64{}, Memories: map[string]int64{}}, false, nil
	}
	if revs.Issues == nil {
		revs.Issues = map[string]int64{}
	}
	if revs.Memories == nil {
		revs.Memories = map[string]int64{}
	}
	return revs, true, nil
}

func writeSyncRevs(dir string, revs syncRevs) error {
	b, err := json.Marshal(revs)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, ".local", syncRevsFile), append(b, '\n'))
}

func readSyncMeta(dir string) syncMeta {
	b, err := os.ReadFile(filepath.Join(dir, ".local", "sync-status.json"))
	if err != nil {
		return syncMeta{}
	}
	var m syncMeta
	_ = json.Unmarshal(b, &m)
	return m
}

func readSyncBase(dir string) (SyncSnapshot, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".local", "sync-base.jsonl"))
	if err != nil {
		return SyncSnapshot{}, err
	}
	var out SyncSnapshot
	err = json.Unmarshal(b, &out)
	return out, err
}
func writeSyncBase(dir string, snapshot SyncSnapshot) error {
	path := filepath.Join(dir, ".local", "sync-base.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(snapshot)
	return atomicWrite(path, append(b, '\n'))
}

func SyncContext(endpoint string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

// migrateLegacyTrackerID moves a legacy UUID tracker_id to its derived short id: it asks
// the server to rename the mirror first and only then rewrites config.yaml. Any failure
// keeps the old id (the sync proceeds as before), so no data is ever lost; the next sync
// retries.
//
// DEPRECATED(v0.126): remove in v0.129 (legacy UUID tracker_id migration).
func migrateLegacyTrackerID(ctx context.Context, s *Store, cfg Config, endpoint, token string) Config {
	if !IsLegacyTrackerID(cfg.TrackerID) {
		return cfg
	}
	oldID, newID := cfg.TrackerID, ShortTrackerID(cfg.TrackerID)
	body, _ := json.Marshal(map[string]string{"new_tracker_id": newID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/tracker/repos/"+url.PathEscape(oldID)+"/rename", bytes.NewReader(body))
	if err != nil {
		return cfg
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: tracker_id migration skipped (%v); syncing with the old id\n", err)
		return cfg
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		fmt.Fprintf(os.Stderr, "warning: tracker_id migration skipped (%s: %s); syncing with the old id\n", resp.Status, strings.TrimSpace(string(b)))
		return cfg
	}
	if err := s.UpdateConfig(func(c *Config) { c.TrackerID = newID }); err != nil {
		// Still sync under the new id: syncing under the old one would recreate the
		// old mirror row and leave both ids behind (the rename then answers 409).
		fmt.Fprintf(os.Stderr, "warning: tracker_id renamed on the server but config.yaml was not updated (%v)\n", err)
	}
	cfg.TrackerID = newID
	return cfg
}
