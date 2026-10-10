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
	// ExternalRef / SpecID carry bd's external_ref and spec_id through a migration.
	ExternalRef string `json:"external_ref,omitempty"`
	SpecID      string `json:"spec_id,omitempty"`
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
	// MemoryMeta (kind / summary / when / expires_at / source / created_at) is
	// flattened after the original fields, all omitempty (memory_meta.go).
	MemoryMeta
}

type Config struct {
	Prefix       string      `yaml:"prefix" json:"prefix"`
	TrackerID    string      `yaml:"tracker_id" json:"tracker_id"`
	ProjectKey   string      `yaml:"project_key,omitempty" json:"project_key,omitempty"`
	CommitPolicy string      `yaml:"commit_policy" json:"commit_policy"`
	AutoSync     bool        `yaml:"auto_sync" json:"auto_sync"`
	Prime        PrimeConfig `yaml:"prime,omitempty" json:"prime,omitempty"`
	Brief        BriefConfig `yaml:"brief,omitempty" json:"brief,omitempty"`
}

// BriefConfig configures `gofer issue brief` for this repository.
type BriefConfig struct {
	// Verify lists the repository's own verification commands. A rule applies when
	// one of its paths globs (MatchMemoryPath syntax) matches a code-entry file of
	// the issue; a rule without paths always applies. Unset = the generic fallback
	// (a plain `go test` hint when the repository has a go.mod).
	Verify []BriefVerifyRule `yaml:"verify,omitempty" json:"verify,omitempty"`
}

// BriefVerifyRule is one `brief.verify` entry. Cmd may carry {dirs} (the
// matched files' directories as ./dir, sorted) and {files} (the matched files).
type BriefVerifyRule struct {
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	Cmd   string   `yaml:"cmd" json:"cmd"`
}

// PrimeConfig uses pointers so an absent section keeps every segment enabled and
// distinguishes an explicit zero limit from the default limit.
type PrimeConfig struct {
	Issues             *bool `yaml:"issues,omitempty" json:"issues,omitempty"`
	Ready              *bool `yaml:"ready,omitempty" json:"ready,omitempty"`
	Memory             *bool `yaml:"memory,omitempty" json:"memory,omitempty"`
	ScopedMemory       *bool `yaml:"scoped_memory,omitempty" json:"scoped_memory,omitempty"`
	Handoff            *bool `yaml:"handoff,omitempty" json:"handoff,omitempty"`
	IssuesLimit        *int  `yaml:"issues_limit,omitempty" json:"issues_limit,omitempty"`
	ReadyLimit         *int  `yaml:"ready_limit,omitempty" json:"ready_limit,omitempty"`
	MemorySummaryLimit *int  `yaml:"memory_summary_limit,omitempty" json:"memory_summary_limit,omitempty"`
	// InjectOnPrompt: the hook injects memories whose when.keywords match a human
	// prompt (UserPromptSubmit, design 2026-10-09 §2.9). Default on.
	InjectOnPrompt *bool `yaml:"inject_on_prompt,omitempty" json:"inject_on_prompt,omitempty"`
	// InjectOnCommand: the PreToolUse hook injects memories whose when.commands
	// prefix the shell command the agent is about to run (§2.9, P5). Default on.
	InjectOnCommand *bool `yaml:"inject_on_command,omitempty" json:"inject_on_command,omitempty"`
	// Focus toggles the auto-generated 「当前重点」 section (design §2.4).
	Focus *bool `yaml:"focus,omitempty" json:"focus,omitempty"`
	// FocusEnv toggles the environment lines of 「当前重点」: the repository line and
	// the server line (server version and the workers serving this project). Default on.
	FocusEnv *bool `yaml:"focus_env,omitempty" json:"focus_env,omitempty"`
	// FocusTag adds 「最近 tag X（之后 N 个提交）」 to the repository line, for
	// repositories that release by tag. Default off.
	FocusTag *bool `yaml:"focus_tag,omitempty" json:"focus_tag,omitempty"`
	// Doctor configures `gofer memory doctor` (and the prime 「⚠ 可能过期」 marker).
	Doctor *PrimeDoctorConfig `yaml:"doctor,omitempty" json:"doctor,omitempty"`
}

// PrimeDoctorConfig silences doctor finding slugs repository-wide (`prime.doctor.suppress`).
type PrimeDoctorConfig struct {
	Suppress []string `yaml:"suppress,omitempty" json:"suppress,omitempty"`
}

// DoctorSuppress is the repository-wide list of silenced doctor slugs.
func (c PrimeConfig) DoctorSuppress() []string {
	if c.Doctor == nil {
		return nil
	}
	return c.Doctor.Suppress
}

func primeEnabled(value *bool) bool { return value == nil || *value }

func (c PrimeConfig) IssuesEnabled() bool       { return primeEnabled(c.Issues) }
func (c PrimeConfig) ReadyEnabled() bool        { return primeEnabled(c.Ready) }
func (c PrimeConfig) MemoryEnabled() bool       { return primeEnabled(c.Memory) }
func (c PrimeConfig) ScopedMemoryEnabled() bool { return primeEnabled(c.ScopedMemory) }
func (c PrimeConfig) HandoffEnabled() bool      { return primeEnabled(c.Handoff) }
func (c PrimeConfig) FocusEnabled() bool        { return primeEnabled(c.Focus) }
func (c PrimeConfig) FocusEnvEnabled() bool     { return primeEnabled(c.FocusEnv) }

// FocusTagEnabled reports prime.focus_tag (default false).
func (c PrimeConfig) FocusTagEnabled() bool { return c.FocusTag != nil && *c.FocusTag }

// InjectOnPromptEnabled reports prime.inject_on_prompt (default true).
func (c PrimeConfig) InjectOnPromptEnabled() bool { return primeEnabled(c.InjectOnPrompt) }

// InjectOnCommandEnabled reports prime.inject_on_command (default true).
func (c PrimeConfig) InjectOnCommandEnabled() bool { return primeEnabled(c.InjectOnCommand) }

func primeLimit(value *int, fallback int) int {
	if value != nil {
		return *value
	}
	return fallback
}

func (c PrimeConfig) ActiveLimit() int  { return primeLimit(c.IssuesLimit, 10) }
func (c PrimeConfig) ReadyCount() int   { return primeLimit(c.ReadyLimit, 10) }
func (c PrimeConfig) SummaryLimit() int { return primeLimit(c.MemorySummaryLimit, -1) }

func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
