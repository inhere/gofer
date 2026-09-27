package tracker

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// MigrateFromBD previews or imports a repository-local bd export. It never
// removes .beads and uses the tracker store's existing locked JSONL writes.
func MigrateFromBD(root string, apply bool) (MigrationReport, error) {
	var report MigrationReport
	issues, err := readBdIssues(filepath.Join(root, ".beads", "issues.jsonl"))
	if err != nil {
		return report, err
	}
	report.Issues = len(issues)
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
	if _, err := exec.LookPath("bd"); err == nil {
		cmd := exec.Command("bd", "memories", "--json", "--readonly")
		cmd.Dir = root
		out, runErr := cmd.Output()
		if runErr != nil {
			report.Notes = append(report.Notes, "bd memories --json unavailable: "+runErr.Error())
		} else {
			var values map[string]string
			if err := json.Unmarshal(out, &values); err != nil {
				return report, fmt.Errorf("bd memories JSON: %w", err)
			}
			report.Memories = len(values)
			if err := store.UpdateMemories(func(existing []Memory) ([]Memory, error) {
				index := make(map[string]bool, len(existing))
				for _, item := range existing {
					index[item.Key] = true
				}
				for key, content := range values {
					if !index[key] {
						existing = append(existing, Memory{Key: key, Content: content, UpdatedAt: Now(), By: "bd-migrate"})
					}
				}
				return existing, nil
			}); err != nil {
				return report, err
			}
			report.Files = append(report.Files, ".gofer/tracker/memories.jsonl")
		}
	} else {
		report.Notes = append(report.Notes, "bd executable absent; memory import skipped")
	}
	if err := migrateManagedBlocks(root, &report); err != nil {
		return report, err
	}
	return report, nil
}

func migrateManagedBlocks(root string, report *MigrationReport) error {
	found := false
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
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
			if strings.Contains(body, beginBlock) {
				replacement = ""
			}
			body = body[:start] + replacement + body[end:]
		} else if !strings.Contains(body, beginBlock) {
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
