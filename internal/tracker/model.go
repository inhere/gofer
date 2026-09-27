// Package tracker owns the repository-local issue and memory JSONL truth.
package tracker

import "time"

// Field order is the JSONL wire order. Keep additions at the end to minimize diffs.
type Issue struct {
	ID                 string      `json:"id"`
	Title              string      `json:"title"`
	Type               string      `json:"type"`
	Status             string      `json:"status"`
	Priority           int         `json:"priority"`
	Description        string      `json:"description,omitempty"`
	Design             string      `json:"design,omitempty"`
	AcceptanceCriteria string      `json:"acceptance_criteria,omitempty"`
	Notes              []NoteEntry `json:"notes,omitempty"`
	Assignee           string      `json:"assignee,omitempty"`
	Owner              string      `json:"owner,omitempty"`
	Tags               []string    `json:"tags,omitempty"`
	Parent             string      `json:"parent,omitempty"`
	Deps               []Dep       `json:"deps,omitempty"`
	Comments           []Comment   `json:"comments,omitempty"`
	CreatedAt          string      `json:"created_at"`
	CreatedBy          string      `json:"created_by,omitempty"`
	UpdatedAt          string      `json:"updated_at,omitempty"`
	StartedAt          string      `json:"started_at,omitempty"`
	ClosedAt           string      `json:"closed_at,omitempty"`
	CloseReason        string      `json:"close_reason,omitempty"`
}

type NoteEntry struct {
	At   string `json:"at"`
	By   string `json:"by"`
	Text string `json:"text"`
}

type Comment struct {
	At   string `json:"at"`
	By   string `json:"by"`
	Text string `json:"text"`
}

type Dep struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Memory struct {
	Key       string   `json:"key"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	By        string   `json:"by"`
}

type Config struct {
	Prefix       string `yaml:"prefix" json:"prefix"`
	TrackerID    string `yaml:"tracker_id" json:"tracker_id"`
	CommitPolicy string `yaml:"commit_policy" json:"commit_policy"`
	AutoSync     bool   `yaml:"auto_sync" json:"auto_sync"`
}

func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
