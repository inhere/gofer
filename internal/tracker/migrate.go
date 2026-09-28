package tracker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type MigrationReport struct {
	Issues   int
	Memories int
	Files    []string
	Notes    []string
}

type bdDependency struct {
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

type bdComment struct {
	Text      string `json:"text"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	By        string `json:"by"`
	At        string `json:"at"`
}

type bdIssue struct {
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
}

func readBdIssues(path string) ([]Issue, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var issues []Issue
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64<<10), 16<<20)
	for line := 1; scan.Scan(); line++ {
		if len(bytes.TrimSpace(scan.Bytes())) == 0 {
			continue
		}
		var source bdIssue
		if err := json.Unmarshal(scan.Bytes(), &source); err != nil {
			return nil, fmt.Errorf("bd issue line %d: %w", line, err)
		}
		if source.RecordType != "issue" {
			continue
		}
		if source.ID == "" {
			return nil, fmt.Errorf("bd issue line %d: missing id", line)
		}
		item := Issue{
			ID: source.ID, Title: source.Title, Type: source.IssueType, Status: source.Status,
			Priority: source.Priority, Description: source.Description, Design: source.Design,
			AcceptanceCriteria: source.AcceptanceCriteria, Assignee: source.Assignee,
			Owner: source.Owner, Tags: addTags(nil, source.Labels), Parent: source.Parent,
			CreatedAt: source.CreatedAt, CreatedBy: source.CreatedBy, UpdatedAt: source.UpdatedAt,
			StartedAt: source.StartedAt, ClosedAt: source.ClosedAt, CloseReason: source.CloseReason,
		}
		if item.Type == "" {
			item.Type = "task"
		}
		if !ValidStatus(item.Status) {
			item.Tags = addTags(item.Tags, []string{"bd:" + item.Status})
			item.Status = "open"
		}
		if item.Parent == "" {
			if dot := strings.LastIndexByte(item.ID, '.'); dot > 0 {
				item.Parent = item.ID[:dot]
			}
		}
		if source.Notes != "" {
			item.Notes = []NoteEntry{{At: item.UpdatedAt, By: item.CreatedBy, Text: source.Notes}}
		}
		for _, dep := range source.Dependencies {
			item.Deps = append(item.Deps, Dep{ID: dep.DependsOnID, Type: dep.Type})
		}
		for _, comment := range source.Comments {
			at, by := comment.CreatedAt, comment.Author
			if at == "" {
				at = comment.At
			}
			if by == "" {
				by = comment.By
			}
			item.Comments = append(item.Comments, Comment{At: at, By: by, Text: comment.Text})
		}
		issues = append(issues, item)
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

// DEPRECATED(v0.68): remove in v0.80, once every workspace has migrated off bd (TRK-01).
// MigrateFromBD previews or imports a repository-local bd export. It never
// removes .beads and uses the tracker store's existing locked JSONL writes.
func MigrateFromBD(root string, apply bool) (MigrationReport, error) {
	var report MigrationReport
	issues, err := readBdIssues(filepath.Join(root, ".beads", "issues.jsonl"))
	if err != nil {
		return report, err
	}
	report.Issues = len(issues)
	// Read every input before writing anything, so a bad bd export cannot leave
	// the repository half migrated (issues imported, blocks and hooks not).
	memories, note, err := readBdMemories(root)
	if err != nil {
		return report, err
	}
	if note != "" {
		report.Notes = append(report.Notes, note)
	}
	report.Memories = len(memories)
	if !apply {
		return report, nil
	}
	store, _, err := Init(root, "", true)
	if err != nil {
		return report, err
	}
	if err := store.UpdateIssues(func(existing []Issue) ([]Issue, error) {
		index := make(map[string]int, len(existing))
		for i := range existing {
			index[existing[i].ID] = i
		}
		for _, incoming := range issues {
			if i, ok := index[incoming.ID]; ok {
				if incoming.UpdatedAt > existing[i].UpdatedAt {
					existing[i] = incoming
				}
			} else {
				index[incoming.ID] = len(existing)
				existing = append(existing, incoming)
			}
		}
		return existing, nil
	}); err != nil {
		return report, err
	}
	report.Files = append(report.Files, ".gofer/tracker/issues.jsonl")
	if len(memories) > 0 {
		if err := store.UpdateMemories(func(existing []Memory) ([]Memory, error) {
			index := make(map[string]bool, len(existing))
			for _, item := range existing {
				index[item.Key] = true
			}
			keys := make([]string, 0, len(memories))
			for key := range memories {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if !index[key] {
					existing = append(existing, Memory{Key: key, Content: memories[key], UpdatedAt: Now(), By: "bd-migrate"})
				}
			}
			return existing, nil
		}); err != nil {
			return report, err
		}
		report.Files = append(report.Files, ".gofer/tracker/memories.jsonl")
	}
	if err := migrateManagedBlocks(root, &report); err != nil {
		return report, err
	}
	return report, nil
}

// readBdMemories runs `bd memories --json` in root. A missing bd or a failing
// command is a note (memories are skipped), a malformed export is an error.
func readBdMemories(root string) (map[string]string, string, error) {
	if _, err := exec.LookPath("bd"); err != nil {
		return nil, "bd executable absent; memory import skipped", nil
	}
	cmd := exec.Command("bd", "memories", "--json", "--readonly")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, "bd memories --json unavailable: " + err.Error(), nil
	}
	values, err := parseBdMemories(out)
	if err != nil {
		return nil, "", fmt.Errorf("bd memories JSON: %w", err)
	}
	return values, "", nil
}

// parseBdMemories reads bd's `{key: content}` map. bd mixes metadata into the
// same object (`"schema_version": 1`), so only string values are memories.
func parseBdMemories(out []byte) (map[string]string, error) {
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

func migrateManagedBlocks(root string, report *MigrationReport) error {
	found := false
	importsAgents := claudeImportsAgents(root)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		// A CLAUDE.md that imports AGENTS.md loses its bd block but gets no
		// gofer block of its own: AGENTS.md already carries it.
		skipBlock := name == "CLAUDE.md" && importsAgents
		path := filepath.Join(root, name)
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		found = true
		body := string(b)
		start := strings.Index(body, "<!-- BEGIN BEADS INTEGRATION")
		if start >= 0 {
			endMark := "<!-- END BEADS INTEGRATION -->"
			end := strings.Index(body[start:], endMark)
			if end < 0 {
				return fmt.Errorf("incomplete BEADS block in %s", path)
			}
			end += start + len(endMark)
			if end < len(body) && body[end] == '\n' {
				end++
			}
			replacement := managedBlock
			if skipBlock || strings.Contains(body, beginBlock) {
				replacement = ""
			}
			body = body[:start] + replacement + body[end:]
		} else if !skipBlock && !strings.Contains(body, beginBlock) {
			if body != "" && !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			body += managedBlock
		}
		if body != string(b) {
			if err := atomicWrite(path, []byte(body)); err != nil {
				return err
			}
			report.Files = append(report.Files, name)
		}
	}
	if !found {
		if err := atomicWrite(filepath.Join(root, "AGENTS.md"), []byte(managedBlock)); err != nil {
			return err
		}
		report.Files = append(report.Files, "AGENTS.md")
	}
	return nil
}
