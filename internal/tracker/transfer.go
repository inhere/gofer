package tracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
)

const transferVersion = 1

// TransferSelection is an exact allowlist. Candidate searches never add records.
type TransferSelection struct {
	IssueIDs   []string `json:"issue_ids"`
	MemoryKeys []string `json:"memory_keys"`
}

type TransferHashes struct {
	Config   string `json:"config"`
	Issues   string `json:"issues"`
	Memories string `json:"memories"`
}

type TransferBoundary struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Reason   string `json:"reason"`
}

// TransferBundle contains complete source JSON objects, including fields a newer
// tracker may know that this binary does not. The source directory is intentionally
// absent: every import names and rechecks its source explicitly.
type TransferBundle struct {
	Version         int                `json:"version"`
	SourceTrackerID string             `json:"source_tracker_id"`
	SourceHashes    TransferHashes     `json:"source_hashes"`
	TargetTrackerID string             `json:"target_tracker_id"`
	TargetPrefix    string             `json:"target_prefix"`
	ProjectKey      string             `json:"project_key"`
	Selection       TransferSelection  `json:"selection"`
	Issues          []json.RawMessage  `json:"issues"`
	Memories        []json.RawMessage  `json:"memories"`
	Boundaries      []TransferBoundary `json:"boundaries"`
	Digest          string             `json:"digest"`
}

type TransferCandidates struct {
	SourceTrackerID string         `json:"source_tracker_id"`
	SourceHashes    TransferHashes `json:"source_hashes"`
	IssueIDs        []string       `json:"issue_ids"`
	MemoryKeys      []string       `json:"memory_keys"`
}

type transferSource struct {
	config    Config
	hashes    TransferHashes
	issues    map[string]Issue
	issueRaw  map[string]json.RawMessage
	memories  map[string]Memory
	memoryRaw map[string]json.RawMessage
}

func transferHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sourceFileHashes(dir string) (TransferHashes, error) {
	var out TransferHashes
	paths := []struct {
		name string
		set  *string
	}{
		{"config.yaml", &out.Config}, {"issues.jsonl", &out.Issues}, {"memories.jsonl", &out.Memories},
	}
	for _, item := range paths {
		data, err := os.ReadFile(filepath.Join(dir, item.name))
		if err != nil {
			return out, fmt.Errorf("read source %s: %w", item.name, err)
		}
		*item.set = transferHash(data)
	}
	return out, nil
}

func readTransferSource(dir string) (*transferSource, error) {
	before, err := sourceFileHashes(dir)
	if err != nil {
		return nil, err
	}
	store := NewStore(dir)
	cfg, err := store.ReadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.TrackerID == "" {
		return nil, errors.New("source tracker_id is empty")
	}
	out := &transferSource{config: cfg, hashes: before, issues: map[string]Issue{}, issueRaw: map[string]json.RawMessage{}, memories: map[string]Memory{}, memoryRaw: map[string]json.RawMessage{}}
	if err := readLines(filepath.Join(dir, "issues.jsonl"), func(raw []byte) error {
		var issue Issue
		if err := json.Unmarshal(raw, &issue); err != nil {
			return err
		}
		if issue.ID == "" {
			return errors.New("issue without id")
		}
		if _, exists := out.issues[issue.ID]; exists {
			return fmt.Errorf("duplicate issue id %q", issue.ID)
		}
		out.issues[issue.ID] = issue
		out.issueRaw[issue.ID] = bytes.Clone(raw)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := readLines(filepath.Join(dir, "memories.jsonl"), func(raw []byte) error {
		var memory Memory
		if err := json.Unmarshal(raw, &memory); err != nil {
			return err
		}
		if memory.Key == "" {
			return errors.New("memory without key")
		}
		if _, exists := out.memories[memory.Key]; exists {
			return fmt.Errorf("duplicate memory key %q", memory.Key)
		}
		out.memories[memory.Key] = memory
		out.memoryRaw[memory.Key] = bytes.Clone(raw)
		return nil
	}); err != nil {
		return nil, err
	}
	after, err := sourceFileHashes(dir)
	if err != nil {
		return nil, err
	}
	if after != before {
		return nil, errors.New("source tracker changed while reading; inspect and export again")
	}
	return out, nil
}

// FindTransferCandidates is read-only discovery. Tags and query are hints; the
// final export still requires an explicit ID/key allowlist.
func FindTransferCandidates(sourceDir, tag, query string) (TransferCandidates, error) {
	source, err := readTransferSource(sourceDir)
	if err != nil {
		return TransferCandidates{}, err
	}
	out := TransferCandidates{SourceTrackerID: source.config.TrackerID, SourceHashes: source.hashes}
	query = strings.ToLower(query)
	for id, issue := range source.issues {
		if tag != "" && !slices.Contains(issue.Tags, tag) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(issue.ID+" "+issue.Title+" "+issue.Description), query) {
			continue
		}
		out.IssueIDs = append(out.IssueIDs, id)
	}
	for key, memory := range source.memories {
		if tag != "" && !slices.Contains(memory.Tags, tag) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(key+" "+memory.Content), query) {
			continue
		}
		out.MemoryKeys = append(out.MemoryKeys, key)
	}
	slices.Sort(out.IssueIDs)
	slices.Sort(out.MemoryKeys)
	return out, nil
}

func normalizeSelection(sel TransferSelection) (TransferSelection, error) {
	if len(sel.IssueIDs)+len(sel.MemoryKeys) == 0 {
		return sel, errors.New("transfer selection is empty")
	}
	sel.IssueIDs = slices.Clone(sel.IssueIDs)
	sel.MemoryKeys = slices.Clone(sel.MemoryKeys)
	slices.Sort(sel.IssueIDs)
	slices.Sort(sel.MemoryKeys)
	for _, list := range [][]string{sel.IssueIDs, sel.MemoryKeys} {
		for i, value := range list {
			if value == "" || i > 0 && value == list[i-1] {
				return sel, fmt.Errorf("empty or duplicate selection value %q", value)
			}
		}
	}
	return sel, nil
}

func transferBoundaries(issues map[string]Issue, selected map[string]bool) []TransferBoundary {
	var out []TransferBoundary
	for id, issue := range issues {
		add := func(to, relation string) {
			if to == "" || selected[id] == selected[to] && issues[to].ID != "" {
				return
			}
			if !selected[id] && !selected[to] {
				return
			}
			reason := "reference crosses selection"
			if _, exists := issues[to]; !exists {
				reason = "referenced issue missing from source"
			}
			out = append(out, TransferBoundary{From: id, To: to, Relation: relation, Reason: reason})
		}
		add(issue.Parent, "parent")
		for _, dep := range issue.Deps {
			add(dep.ID, "dep:"+dep.Type)
		}
	}
	slices.SortFunc(out, func(a, b TransferBoundary) int {
		return strings.Compare(a.From+"\x00"+a.Relation+"\x00"+a.To, b.From+"\x00"+b.Relation+"\x00"+b.To)
	})
	return out
}

func prepareTransfer(source *transferSource, sel TransferSelection, targetID, prefix, projectKey string) (TransferBundle, error) {
	var err error
	sel, err = normalizeSelection(sel)
	if err != nil {
		return TransferBundle{}, err
	}
	if prefix == "" || strings.ContainsAny(prefix, "/\\ \t\r\n") || projectKey == "" || targetID == "" || targetID == source.config.TrackerID {
		return TransferBundle{}, errors.New("target tracker_id (different from source), valid prefix, and project_key are required")
	}
	out := TransferBundle{Version: transferVersion, SourceTrackerID: source.config.TrackerID, SourceHashes: source.hashes, TargetTrackerID: targetID, TargetPrefix: prefix, ProjectKey: projectKey, Selection: sel}
	selected := make(map[string]bool, len(sel.IssueIDs))
	for _, id := range sel.IssueIDs {
		raw, ok := source.issueRaw[id]
		if !ok {
			return TransferBundle{}, fmt.Errorf("selected issue %q not found", id)
		}
		selected[id] = true
		out.Issues = append(out.Issues, raw)
	}
	for _, key := range sel.MemoryKeys {
		raw, ok := source.memoryRaw[key]
		if !ok {
			return TransferBundle{}, fmt.Errorf("selected memory %q not found", key)
		}
		out.Memories = append(out.Memories, raw)
	}
	out.Boundaries = transferBoundaries(source.issues, selected)
	out.Digest, err = transferDigest(out)
	return out, err
}

func transferDigest(bundle TransferBundle) (string, error) {
	bundle.Digest = ""
	data, err := json.Marshal(bundle)
	if err != nil {
		return "", err
	}
	return transferHash(data), nil
}

// PrepareTransfer freezes a reviewable package; it never writes the source.
// Boundaries stay in the package so a reviewer can extend the allowlist.
func PrepareTransfer(sourceDir string, sel TransferSelection, prefix, projectKey string) (TransferBundle, error) {
	source, err := readTransferSource(sourceDir)
	if err != nil {
		return TransferBundle{}, err
	}
	return prepareTransfer(source, sel, uuid.NewString(), prefix, projectKey)
}

func pathWithin(base, path string) bool {
	if runtime.GOOS == "windows" {
		base, path = strings.ToLower(base), strings.ToLower(path)
	}
	rel, err := filepath.Rel(base, path)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// checkTransferOutput keeps the reviewed bundle out of the source tracker,
// including a path reached through a symlinked parent or Windows case alias.
func checkTransferOutput(sourceDir, path string) error {
	source, err := filepath.EvalSymlinks(sourceDir)
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	output, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if pathWithin(source, output) {
		return errors.New("bundle output must be outside source tracker")
	}
	if info, err := os.Lstat(output); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("bundle output must not be a symlink")
		}
		resolved, err := filepath.EvalSymlinks(output)
		if err != nil {
			return err
		}
		if pathWithin(source, resolved) {
			return errors.New("bundle output aliases source tracker")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(output)
	var tail []string
	for {
		if _, err := os.Lstat(parent); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tail = append(tail, filepath.Base(parent))
		next := filepath.Dir(parent)
		if next == parent {
			return errors.New("bundle output parent cannot be resolved")
		}
		parent = next
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	for i := len(tail) - 1; i >= 0; i-- {
		resolvedParent = filepath.Join(resolvedParent, tail[i])
	}
	if pathWithin(source, filepath.Join(resolvedParent, filepath.Base(output))) {
		return errors.New("bundle output aliases source tracker")
	}
	return nil
}

func WriteTransferBundle(sourceDir, path string, bundle TransferBundle) error {
	if err := checkTransferOutput(sourceDir, path); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}

func ReadTransferBundle(path string) (TransferBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return TransferBundle{}, err
	}
	var bundle TransferBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return bundle, err
	}
	digest, err := transferDigest(bundle)
	if err != nil {
		return bundle, err
	}
	if bundle.Version != transferVersion || digest != bundle.Digest {
		return bundle, errors.New("transfer bundle version or digest mismatch")
	}
	return bundle, nil
}

type transferReceipt struct {
	BundleDigest string         `json:"bundle_digest"`
	FileHashes   TransferHashes `json:"file_hashes"`
}

func rawLines(items []json.RawMessage) ([]byte, error) {
	var out bytes.Buffer
	for _, item := range items {
		if err := json.Compact(&out, item); err != nil {
			return nil, err
		}
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func checkPublishedTarget(dir string, bundle TransferBundle) error {
	data, err := os.ReadFile(filepath.Join(dir, "transfer.json"))
	if err != nil {
		return fmt.Errorf("target tracker exists without matching transfer receipt: %w", err)
	}
	var receipt transferReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return err
	}
	if receipt.BundleDigest != bundle.Digest {
		return errors.New("target tracker conflicts with transfer bundle")
	}
	hashes, err := sourceFileHashes(dir)
	if err != nil {
		return err
	}
	if hashes != receipt.FileHashes {
		return errors.New("target tracker changed after transfer; refusing idempotent import")
	}
	return nil
}

// ImportTransfer stages an entire tracker under targetRoot/.gofer, verifies it,
// then publishes its directory. Source freshness is checked both before and
// immediately before publish. It never removes source records or syncs.
func ImportTransfer(sourceDir, targetRoot string, bundle TransferBundle) (published bool, resultErr error) {
	digest, err := transferDigest(bundle)
	if err != nil || bundle.Version != transferVersion || digest != bundle.Digest {
		return false, errors.New("transfer bundle version or digest mismatch")
	}
	source, err := readTransferSource(sourceDir)
	if err != nil {
		return false, err
	}
	if source.hashes != bundle.SourceHashes || source.config.TrackerID != bundle.SourceTrackerID {
		return false, errors.New("source tracker changed since export; export again")
	}
	want, err := prepareTransfer(source, bundle.Selection, bundle.TargetTrackerID, bundle.TargetPrefix, bundle.ProjectKey)
	if err != nil {
		return false, err
	}
	if want.Digest != bundle.Digest {
		return false, errors.New("transfer bundle differs from current source selection")
	}
	if len(bundle.Boundaries) > 0 {
		return false, fmt.Errorf("transfer has %d unresolved issue references; extend the allowlist", len(bundle.Boundaries))
	}
	root, err := filepath.Abs(targetRoot)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return false, fmt.Errorf("target repository root must exist: %s", root)
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return false, fmt.Errorf("target root must be an existing Git repository: %w", err)
	}
	parent := filepath.Join(root, ".gofer")
	target := filepath.Join(parent, "tracker")
	sourceAbs, err := filepath.Abs(sourceDir)
	if err != nil {
		return false, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	realSource, err := filepath.EvalSymlinks(sourceAbs)
	if err != nil {
		return false, err
	}
	realTarget := filepath.Join(realRoot, ".gofer", "tracker")
	equalPath := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
		}
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if equalPath(realSource, realTarget) {
		return false, errors.New("source and target tracker are the same directory")
	}
	if rel, err := filepath.Rel(realSource, realRoot); err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return false, errors.New("target root cannot be inside source tracker")
	}
	if info, err := os.Lstat(parent); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("target .gofer directory must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if targetInfo, err := os.Lstat(target); err == nil {
		if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("target tracker path already exists and is not an owned directory")
		}
		if err := checkPublishedTarget(target, bundle); err != nil {
			return false, err
		}
		return false, appendOnce(filepath.Join(parent, ".gitignore"), "tracker/.local/\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return false, err
	}
	stage, err := os.MkdirTemp(parent, ".tracker-stage-")
	if err != nil {
		return false, err
	}
	// stage came from MkdirTemp in parent; check its containment before cleanup.
	if rel, relErr := filepath.Rel(parent, stage); relErr != nil || strings.HasPrefix(rel, "..") || filepath.Dir(stage) != parent {
		return false, errors.New("unsafe transfer stage path")
	}
	defer os.RemoveAll(stage)
	cfg := Config{Prefix: bundle.TargetPrefix, TrackerID: bundle.TargetTrackerID, ProjectKey: bundle.ProjectKey, CommitPolicy: "local-commit", AutoSync: false}
	configData, err := yaml.Marshal(cfg)
	if err != nil {
		return false, err
	}
	issuesData, err := rawLines(bundle.Issues)
	if err != nil {
		return false, err
	}
	memoriesData, err := rawLines(bundle.Memories)
	if err != nil {
		return false, err
	}
	for _, item := range []struct {
		name string
		data []byte
	}{{"config.yaml", configData}, {"issues.jsonl", issuesData}, {"memories.jsonl", memoriesData}} {
		if err := atomicWrite(filepath.Join(stage, item.name), item.data); err != nil {
			return false, err
		}
	}
	stageStore := NewStore(stage)
	issues, err := stageStore.ReadIssues()
	if err != nil || len(issues) != len(bundle.Issues) {
		return false, fmt.Errorf("staged issues verification failed: %v", err)
	}
	memories, err := stageStore.ReadMemories()
	if err != nil || len(memories) != len(bundle.Memories) {
		return false, fmt.Errorf("staged memories verification failed: %v", err)
	}
	stagedCfg, err := stageStore.ReadConfig()
	if err != nil || stagedCfg.TrackerID != bundle.TargetTrackerID || stagedCfg.AutoSync {
		return false, fmt.Errorf("staged config verification failed: %v", err)
	}
	hashes, err := sourceFileHashes(stage)
	if err != nil {
		return false, err
	}
	if hashes != (TransferHashes{Config: transferHash(configData), Issues: transferHash(issuesData), Memories: transferHash(memoriesData)}) {
		return false, errors.New("staged tracker files differ from transfer bundle")
	}
	receiptData, err := json.Marshal(transferReceipt{BundleDigest: bundle.Digest, FileHashes: hashes})
	if err != nil {
		return false, err
	}
	if err := atomicWrite(filepath.Join(stage, "transfer.json"), append(receiptData, '\n')); err != nil {
		return false, err
	}
	// Cooperating source writers use the same lock. Hold it through publish so
	// the final hash cannot become stale in the last rename window.
	lock, err := NewStore(sourceDir).AcquireLock()
	if err != nil {
		return false, err
	}
	defer func() {
		if releaseErr := lock.Release(); resultErr == nil && releaseErr != nil {
			resultErr = releaseErr
		}
	}()
	fresh, err := sourceFileHashes(sourceDir)
	if err != nil || fresh != bundle.SourceHashes {
		return false, errors.New("source tracker changed before publish; export again")
	}
	if targetInfo, err := os.Lstat(target); err == nil {
		if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("target tracker path already exists and is not an owned directory")
		}
		if err := checkPublishedTarget(target, bundle); err != nil {
			return false, err
		}
		return false, appendOnce(filepath.Join(parent, ".gitignore"), "tracker/.local/\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.Rename(stage, target); err != nil {
		// Another importer may have published the same bundle after our last
		// existence check. Treat that as the same idempotent result.
		if targetInfo, statErr := os.Lstat(target); statErr == nil && targetInfo.IsDir() && targetInfo.Mode()&os.ModeSymlink == 0 {
			if checkErr := checkPublishedTarget(target, bundle); checkErr == nil {
				return false, appendOnce(filepath.Join(parent, ".gitignore"), "tracker/.local/\n")
			}
		}
		return false, fmt.Errorf("publish target tracker: %w", err)
	}
	if err := appendOnce(filepath.Join(parent, ".gitignore"), "tracker/.local/\n"); err != nil {
		return true, fmt.Errorf("tracker published but .gitignore update failed: %w", err)
	}
	return true, nil
}
