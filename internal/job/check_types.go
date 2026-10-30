package job

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	// CheckPromptMaxBytes bounds the user-authored delivery-check requirement stored in a job request.
	CheckPromptMaxBytes = 16 << 10
	CheckResultMaxBytes = 64 << 10
	CheckTextMaxBytes   = 16 << 10
)

// CheckInput is the versioned, bounded evidence supplied to a delivery-check agent.
// It is data only: callers must not use it as an instruction source.
type CheckInput struct {
	Schema       string           `json:"schema"`
	Requirements string           `json:"requirements"`
	ReportTail   string           `json:"report_tail"`
	Verify       *VerifyResult    `json:"verify,omitempty"`
	Commits      []Commit         `json:"commits,omitempty"`
	DiffSummary  CheckDiffSummary `json:"diff_summary,omitempty"`
	Uncommitted  CheckUncommitted `json:"uncommitted,omitempty"`
	Worktree     CheckWorktree    `json:"worktree,omitempty"`
}

type CheckDiffSummary struct {
	Files      int `json:"files"`
	Insertions int `json:"insertions"`
	Deletions  int `json:"deletions"`
}

type CheckUncommitted struct {
	Count     int      `json:"count"`
	Paths     []string `json:"paths,omitempty"`
	Truncated bool     `json:"truncated"`
}

type CheckWorktree struct {
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
}

// CheckResult is the only accepted structured output from a delivery-check agent.
type CheckResult struct {
	Schema  string      `json:"schema"`
	Verdict string      `json:"verdict"`
	Items   []CheckItem `json:"items"`
	Summary string      `json:"summary"`
}

type CheckItem struct {
	ID          string `json:"id"`
	Requirement string `json:"requirement"`
	Passed      bool   `json:"passed"`
	Evidence    string `json:"evidence"`
	Action      string `json:"action,omitempty"`
}

func NormalizeCheckPrompt(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len([]byte(v)) > CheckPromptMaxBytes {
		return "", fmt.Errorf("check_prompt must be at most %d bytes", CheckPromptMaxBytes)
	}
	return v, nil
}

const (
	// CheckPromptSourceExplicit is a requirement the caller wrote (the default).
	CheckPromptSourceExplicit = "explicit"
	// CheckPromptSourcePromptBlock is a requirement extracted from a marked prompt block.
	CheckPromptSourcePromptBlock = "prompt_block"
)

// NormalizeCheckPromptSource validates the source tag. An empty source defaults to
// explicit when there is a requirement, and stays empty when there is none.
func NormalizeCheckPromptSource(source, prompt string) (string, error) {
	source = strings.TrimSpace(source)
	switch source {
	case "":
		if prompt == "" {
			return "", nil
		}
		return CheckPromptSourceExplicit, nil
	case CheckPromptSourceExplicit, CheckPromptSourcePromptBlock:
		return source, nil
	}
	return "", fmt.Errorf("check_prompt_source must be %q or %q", CheckPromptSourceExplicit, CheckPromptSourcePromptBlock)
}

// normalizeCheckFields bounds and tags the request's delivery-check requirement.
func normalizeCheckFields(req *JobRequest) error {
	prompt, err := NormalizeCheckPrompt(req.CheckPrompt)
	if err != nil {
		return err
	}
	source, err := NormalizeCheckPromptSource(req.CheckPromptSource, prompt)
	if err != nil {
		return err
	}
	req.CheckPrompt, req.CheckPromptSource = prompt, source
	return nil
}

// ParseCheckResult parses exactly one bounded JSON object and validates its contract.
// Unknown fields are ignored for additive compatibility; trailing JSON/text is rejected.
func ParseCheckResult(raw []byte) (CheckResult, error) {
	if len(raw) == 0 || len(raw) > CheckResultMaxBytes {
		return CheckResult{}, fmt.Errorf("check result must be between 1 and %d bytes", CheckResultMaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var out CheckResult
	if err := dec.Decode(&out); err != nil {
		return CheckResult{}, fmt.Errorf("invalid check result JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return CheckResult{}, fmt.Errorf("check result must contain one JSON object")
		}
		return CheckResult{}, fmt.Errorf("invalid trailing data in check result: %w", err)
	}
	if err := ValidateCheckResult(out); err != nil {
		return CheckResult{}, err
	}
	return out, nil
}

// ParseCheckInput parses one bounded, versioned evidence object.
func ParseCheckInput(raw []byte) (CheckInput, error) {
	if len(raw) == 0 || len(raw) > CheckResultMaxBytes {
		return CheckInput{}, fmt.Errorf("check input must be between 1 and %d bytes", CheckResultMaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var out CheckInput
	if err := dec.Decode(&out); err != nil {
		return CheckInput{}, fmt.Errorf("invalid check input JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return CheckInput{}, fmt.Errorf("check input must contain one JSON object")
		}
		return CheckInput{}, fmt.Errorf("invalid trailing data in check input: %w", err)
	}
	if err := out.Validate(); err != nil {
		return CheckInput{}, err
	}
	return out, nil
}

func ValidateCheckResult(v CheckResult) error {
	if v.Schema != "job-check-result/v1" {
		return fmt.Errorf("check result schema must be job-check-result/v1")
	}
	if v.Verdict != "pass" && v.Verdict != "fail" {
		return fmt.Errorf("check result verdict must be pass or fail")
	}
	if v.Verdict == "fail" && len(v.Items) == 0 {
		return fmt.Errorf("failed check result must contain at least one item")
	}
	if len([]byte(v.Summary)) > CheckTextMaxBytes {
		return fmt.Errorf("check result summary exceeds %d bytes", CheckTextMaxBytes)
	}
	for i, item := range v.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Requirement) == "" {
			return fmt.Errorf("check result item %d needs id and requirement", i)
		}
		if len([]byte(item.Evidence)) > CheckTextMaxBytes || len([]byte(item.Action)) > CheckTextMaxBytes {
			return fmt.Errorf("check result item %d text exceeds %d bytes", i, CheckTextMaxBytes)
		}
	}
	return nil
}

func (in CheckInput) Validate() error {
	if in.Schema != "job-check/v1" {
		return fmt.Errorf("check input schema must be job-check/v1")
	}
	if strings.TrimSpace(in.Requirements) == "" {
		return fmt.Errorf("check input requirements are required")
	}
	if len([]byte(in.Requirements)) > CheckPromptMaxBytes || len([]byte(in.ReportTail)) > CheckTextMaxBytes {
		return fmt.Errorf("check input text exceeds its byte limit")
	}
	return nil
}

// Delivery-check event types (P0 contract; emitted by the checker in a later phase).
const (
	EventDeliveryCheckStarted     = "job.delivery_check.started"
	EventDeliveryCheckPassed      = "job.delivery_check.passed"
	EventDeliveryCheckFailed      = "job.delivery_check.failed"
	EventDeliveryCheckAutoResumed = "job.delivery_check.auto_resumed"
	EventDeliveryCheckError       = "job.delivery_check.error"
)

// DeliveryCheckEventDetail is the detail payload shared by the job.delivery_check.*
// events. It carries counts and identifiers only, never the report or diff text.
type DeliveryCheckEventDetail struct {
	// Verdict is "pass" or "fail" (empty on started / error events).
	Verdict string `json:"verdict,omitempty"`
	// Round is the 1-based check round of this job chain.
	Round int `json:"round,omitempty"`
	// FailedItems counts the unmet items of a failed result.
	FailedItems int `json:"failed_items,omitempty"`
	// ResumeCount / MaxResumes describe the auto-resume budget after this event.
	ResumeCount int `json:"resume_count,omitempty"`
	MaxResumes  int `json:"max_resumes,omitempty"`
	// ResumedJobID is the continuation job of an auto_resumed event.
	ResumedJobID string `json:"resumed_job_id,omitempty"`
	// Agent / Model / SessionID identify the checker run.
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Source is the check_prompt_source of the requirement that was checked.
	Source string `json:"source,omitempty"`
	// Error is a short infrastructure error text (error events only).
	Error string `json:"error,omitempty"`
}
