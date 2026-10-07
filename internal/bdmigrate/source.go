package bdmigrate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/tracker"
)

type bdDependency struct {
	IssueID     string          `json:"issue_id"`
	DependsOnID string          `json:"depends_on_id"`
	Type        string          `json:"type"`
	CreatedAt   string          `json:"created_at"`
	CreatedBy   string          `json:"created_by"`
	Metadata    json.RawMessage `json:"metadata"`
}

type bdComment struct {
	ID        json.RawMessage `json:"id"`
	Text      string          `json:"text"`
	Author    string          `json:"author"`
	CreatedAt string          `json:"created_at"`
	By        string          `json:"by"`
	At        string          `json:"at"`
}

// bdRecord is one line of `bd export` / .beads/issues.jsonl. Issues carry
// `_type: "issue"` (older exports omit it), memories `_type: "memory"`.
type bdRecord struct {
	RecordType         string         `json:"_type"`
	ID                 string         `json:"id"`
	Title              string         `json:"title"`
	Status             string         `json:"status"`
	Priority           int            `json:"priority"`
	IssueType          string         `json:"issue_type"`
	Description        string         `json:"description"`
	Design             string         `json:"design"`
	AcceptanceCriteria string         `json:"acceptance_criteria"`
	Assignee           string         `json:"assignee"`
	Owner              string         `json:"owner"`
	Labels             []string       `json:"labels"`
	Parent             string         `json:"parent"`
	Dependencies       []bdDependency `json:"dependencies"`
	Comments           []bdComment    `json:"comments"`
	Notes              string         `json:"notes"`
	CreatedAt          string         `json:"created_at"`
	CreatedBy          string         `json:"created_by"`
	UpdatedAt          string         `json:"updated_at"`
	StartedAt          string         `json:"started_at"`
	ClosedAt           string         `json:"closed_at"`
	CloseReason        string         `json:"close_reason"`
	ExternalRef        string         `json:"external_ref"`
	SpecID             string         `json:"spec_id"`
	LeaseExpiresAt     string         `json:"lease_expires_at"`
	HeartbeatAt        string         `json:"heartbeat_at"`
	// memory records
	Key   string `json:"key"`
	Value string `json:"value"`
}

// dataset is everything read from one bd source.
type dataset struct {
	issues   []bdRecord
	memories map[string]string
	skipped  map[string]int // record types ignored, by _type
}

func parseRecords(r *bytes.Reader, what string) (dataset, error) {
	ds := dataset{memories: map[string]string{}, skipped: map[string]int{}}
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 64<<10), 64<<20)
	for line := 1; scan.Scan(); line++ {
		text := bytes.TrimSpace(scan.Bytes())
		if len(text) == 0 {
			continue
		}
		if text[0] != '{' {
			continue // bd prints warnings on stdout in some modes
		}
		var rec bdRecord
		if err := json.Unmarshal(text, &rec); err != nil {
			return ds, fmt.Errorf("%s line %d: %w", what, line, err)
		}
		switch rec.RecordType {
		case "issue", "":
			if rec.ID == "" {
				return ds, fmt.Errorf("%s line %d: issue without id", what, line)
			}
			ds.issues = append(ds.issues, rec)
		case "memory":
			if rec.Key != "" {
				ds.memories[rec.Key] = rec.Value
			}
		default:
			ds.skipped[rec.RecordType]++
		}
	}
	return ds, scan.Err()
}

func readJSONL(path string) (dataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return dataset{}, err
	}
	return parseRecords(bytes.NewReader(b), filepath.Base(path))
}

// childMarker is the env var set on every bd process gofer starts.
const childMarker = "GOFER_BDMIGRATE_CHILD=1"

// settleLimit bounds how long run waits for bd's leftover helpers.
const settleLimit = 3 * time.Second

// bdRunner executes the bd binary. Only read-only invocations are ever built.
type bdRunner struct {
	bin     string
	root    string
	timeout time.Duration
	notes   []string
}

func (r *bdRunner) run(env []string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, args...)
	procattr.Background(cmd)
	cmd.Dir = r.root
	// childMarker tags this bd and everything it forks, so the activity guard can
	// tell gofer's own export processes from a live bd someone else is running.
	cmd.Env = append(append(os.Environ(), childMarker), env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	// Run reaps bd itself, but bd may leave a short-lived helper behind; give it a
	// moment to exit so the next command starts from a quiet repository.
	settleChildren(r.root, settleLimit)
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", r.timeout)
	}
	return so.Bytes(), se.Bytes(), err
}

// exportLive asks bd for the current database content, including memories.
// `--readonly` guarantees the real repository is not written. An older
// database schema is refused by read-only opens; the retry with
// BD_IGNORE_SCHEMA_SKEW=1 reads it anyway and is recorded in the notes.
func (r *bdRunner) exportLive() (dataset, error) {
	args := []string{"--readonly", "export", "--include-memories"}
	out, errOut, err := r.run(nil, args...)
	if err != nil && bytes.Contains(errOut, []byte("schema version mismatch")) {
		r.notes = append(r.notes, "bd database schema is older than the bd binary; exported read-only with BD_IGNORE_SCHEMA_SKEW=1")
		out, errOut, err = r.run([]string{"BD_IGNORE_SCHEMA_SKEW=1"}, args...)
	}
	if err != nil {
		return dataset{}, fmt.Errorf("bd export failed: %w: %s", err, firstLine(errOut))
	}
	return parseRecords(bytes.NewReader(out), "bd export")
}

// memoriesLive is the fallback memory source (`bd memories --json`).
func (r *bdRunner) memoriesLive() (map[string]string, error) {
	out, errOut, err := r.run(nil, "--readonly", "memories", "--json")
	if err != nil && bytes.Contains(errOut, []byte("schema version mismatch")) {
		out, errOut, err = r.run([]string{"BD_IGNORE_SCHEMA_SKEW=1"}, "--readonly", "memories", "--json")
	}
	if err != nil {
		return nil, fmt.Errorf("bd memories --json failed: %w: %s", err, firstLine(errOut))
	}
	return parseBdMemories(out)
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// parseBdMemories reads bd's `{key: content}` map. bd mixes metadata into the
// same object (`"schema_version": 1`), so only string values are memories.
func parseBdMemories(out []byte) (map[string]string, error) {
	// bd may print warnings before the JSON object.
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:]
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(raw))
	for key, value := range raw {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) == 0 || trimmed[0] != '"' {
			continue // metadata such as schema_version, or null
		}
		var content string
		if json.Unmarshal(trimmed, &content) == nil {
			values[key] = content
		}
	}
	return values, nil
}

// SourceReport says where the data came from and how the two bd views differ.
type SourceReport struct {
	Used         string   `json:"used"` // "bd export" | "issues.jsonl"
	LiveIssues   int      `json:"live_issues"`
	LiveMemories int      `json:"live_memories"`
	JSONLIssues  int      `json:"jsonl_issues"`
	JSONLPresent bool     `json:"jsonl_present"`
	OnlyLive     []string `json:"only_in_live,omitempty"`
	OnlyJSONL    []string `json:"only_in_jsonl,omitempty"`
	NewerLive    []string `json:"newer_in_live,omitempty"` // same id, different updated_at
	Stale        bool     `json:"jsonl_stale"`
}

// compareSources fills the difference fields. Lists are capped at 20 ids.
func compareSources(live, file dataset, rep *SourceReport) {
	liveByID := map[string]bdRecord{}
	for _, rec := range live.issues {
		liveByID[rec.ID] = rec
	}
	fileByID := map[string]bdRecord{}
	for _, rec := range file.issues {
		fileByID[rec.ID] = rec
	}
	for id, rec := range liveByID {
		old, ok := fileByID[id]
		switch {
		case !ok:
			rep.OnlyLive = append(rep.OnlyLive, id)
		case old.UpdatedAt != rec.UpdatedAt:
			rep.NewerLive = append(rep.NewerLive, id)
		}
	}
	for id := range fileByID {
		if _, ok := liveByID[id]; !ok {
			rep.OnlyJSONL = append(rep.OnlyJSONL, id)
		}
	}
	sort.Strings(rep.OnlyLive)
	sort.Strings(rep.OnlyJSONL)
	sort.Strings(rep.NewerLive)
	rep.Stale = len(rep.OnlyLive)+len(rep.OnlyJSONL)+len(rep.NewerLive) > 0
}

// Conversion statistics, reported so a human can see what the mapping did.
type Mapping struct {
	StatusRemapped   map[string]int `json:"status_remapped,omitempty"` // bd status -> count (kept as tag bd:<status>, status open)
	ParentFromDep    int            `json:"parent_from_parent_child_dep"`
	ParentFromDotID  int            `json:"parent_from_dotted_id"`
	ExtraParentDeps  int            `json:"extra_parent_child_deps_kept"`
	DepTypes         map[string]int `json:"dep_types,omitempty"`
	DanglingDeps     int            `json:"dangling_deps"`
	LeasesDropped    int            `json:"lease_fields_dropped"`
	DepMetaDropped   int            `json:"dep_created_fields_dropped"`
	NotesConverted   int            `json:"notes_converted"`
	CommentsImported int            `json:"comments"`
}

// convertIssues maps bd records to tracker issues without loss of the fields
// gofer models. Dropped on purpose: the lease/heartbeat claim timestamps (a
// transient bd worker lease) and the created_at/created_by/metadata of each
// dependency edge.
func convertIssues(records []bdRecord) ([]tracker.Issue, Mapping) {
	m := Mapping{StatusRemapped: map[string]int{}, DepTypes: map[string]int{}}
	known := make(map[string]bool, len(records))
	for _, rec := range records {
		known[rec.ID] = true
	}
	out := make([]tracker.Issue, 0, len(records))
	for _, src := range records {
		item := tracker.Issue{
			ID: src.ID, Title: src.Title, Type: src.IssueType, Status: src.Status,
			Priority: src.Priority, Description: src.Description, Design: src.Design,
			AcceptanceCriteria: src.AcceptanceCriteria, Assignee: src.Assignee,
			Owner: src.Owner, Tags: cleanTags(src.Labels), Parent: src.Parent,
			CreatedAt: src.CreatedAt, CreatedBy: src.CreatedBy, UpdatedAt: src.UpdatedAt,
			StartedAt: src.StartedAt, ClosedAt: src.ClosedAt, CloseReason: src.CloseReason,
			ExternalRef: src.ExternalRef, SpecID: src.SpecID,
		}
		if item.Type == "" {
			item.Type = "task"
		}
		if item.UpdatedAt == "" {
			item.UpdatedAt = item.CreatedAt
		}
		if !tracker.ValidStatus(item.Status) {
			m.StatusRemapped[item.Status]++
			item.Tags = cleanTags(append(item.Tags, "bd:"+item.Status))
			item.Status = "open"
		}
		if src.LeaseExpiresAt != "" || src.HeartbeatAt != "" {
			m.LeasesDropped++
		}
		// Parent comes from the parent-child dependency (the only parent
		// representation bd exports); the first one wins, extra ones stay as deps.
		if item.Parent == "" {
			// With several parent-child edges prefer the one the dotted id names.
			dotted := ""
			if dot := strings.LastIndexByte(item.ID, '.'); dot > 0 {
				dotted = item.ID[:dot]
			}
			for _, dep := range src.Dependencies {
				if dep.Type == "parent-child" && (item.Parent == "" || dep.DependsOnID == dotted) {
					item.Parent = dep.DependsOnID
				}
			}
			if item.Parent != "" {
				m.ParentFromDep++
			}
		}
		for _, dep := range src.Dependencies {
			m.DepTypes[dep.Type]++
			if !known[dep.DependsOnID] {
				m.DanglingDeps++
			}
			if dep.CreatedAt != "" || dep.CreatedBy != "" || metadataSet(dep.Metadata) {
				m.DepMetaDropped++
			}
			if dep.Type == "parent-child" && dep.DependsOnID == item.Parent {
				continue
			}
			if dep.Type == "parent-child" {
				m.ExtraParentDeps++
			}
			item.Deps = append(item.Deps, tracker.Dep{ID: dep.DependsOnID, Type: dep.Type})
		}
		if item.Parent == "" {
			if dot := strings.LastIndexByte(item.ID, '.'); dot > 0 && known[item.ID[:dot]] {
				item.Parent = item.ID[:dot]
				m.ParentFromDotID++
			}
		}
		if src.Notes != "" {
			item.Notes = []tracker.NoteEntry{{At: item.UpdatedAt, By: item.CreatedBy, Text: src.Notes}}
			m.NotesConverted++
		}
		for _, c := range src.Comments {
			at, by := c.CreatedAt, c.Author
			if at == "" {
				at = c.At
			}
			if by == "" {
				by = c.By
			}
			item.Comments = append(item.Comments, tracker.Comment{At: at, By: by, Text: c.Text})
			m.CommentsImported++
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(m.StatusRemapped) == 0 {
		m.StatusRemapped = nil
	}
	return out, m
}

func cleanTags(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// inferPrefix picks the dominant issue-id prefix ("h-aii" for "h-aii-0hl8.4").
func inferPrefix(records []bdRecord) string {
	count := map[string]int{}
	for _, rec := range records {
		id := rec.ID
		if dot := strings.IndexByte(id, '.'); dot > 0 {
			id = id[:dot]
		}
		if dash := strings.LastIndexByte(id, '-'); dash > 0 {
			count[id[:dash]]++
		}
	}
	best, bestN := "", 0
	for p, n := range count {
		if n > bestN || (n == bestN && p < best) {
			best, bestN = p, n
		}
	}
	return best
}

// metadataSet reports whether a dependency's metadata holds anything (bd writes
// an empty `{}`, sometimes as a JSON string).
func metadataSet(raw json.RawMessage) bool {
	text := string(bytes.TrimSpace(raw))
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		text = strings.TrimSpace(asString)
	}
	return text != "" && text != "{}" && text != "null"
}
