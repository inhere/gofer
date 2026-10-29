package work

import (
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// The capture box's quick-record and target-suggestion entry points (design 2026-10-16 §3).
// The status rules (a captured item is a person's, nothing automatic touches it) live in
// jobstore and the consumers; this file is only the service face the handlers call.

// DefaultCaptureKind is the kind of a capture that names none: a plain note is the least
// presumptuous label (an idea / issue / request is a claim about what the text is).
const DefaultCaptureKind = jobstore.WorkKindNote

// CaptureInput is one quick record.
type CaptureInput struct {
	Text      string
	Kind      string // idea | issue | request | note; empty = DefaultCaptureKind
	Project   string
	Workspace string
	// SourceSession / SourceJob are recorded as fields only; By is the journal author.
	SourceSession string
	SourceJob     string
	By            string
}

// Capture records a captured item (status captured, status_source human). The kind is
// validated by the store (jobstore.ErrWorkInvalid).
func (s *Service) Capture(in CaptureInput) (jobstore.WorkItem, error) {
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = DefaultCaptureKind
	}
	return s.store.CaptureWorkItem(jobstore.CaptureInput{
		Text: in.Text, Kind: kind, ProjectKey: in.Project, Workspace: in.Workspace,
		SourceSession: in.SourceSession, SourceJob: in.SourceJob, By: in.By,
	})
}

// TargetQuery is what the session recommendation matches an item by.
type TargetQuery struct {
	Text    string
	Project string
}

// TargetQuery returns the text and project of item id for the recommendation. An item that
// is not captured is allowed too: the same text / project rule applies. ErrWorkItemNotFound
// for an unknown id.
func (s *Service) TargetQuery(id string) (TargetQuery, error) {
	w, ok, err := s.store.GetWorkItem(id)
	if err != nil {
		return TargetQuery{}, err
	}
	if !ok {
		return TargetQuery{}, jobstore.ErrWorkItemNotFound
	}
	text := strings.TrimSpace(w.CaptureText)
	if text == "" {
		text = w.Title
	}
	parts := []string{text}
	if g := strings.TrimSpace(w.Goal); g != "" && g != text {
		parts = append(parts, g)
	}
	return TargetQuery{Text: strings.Join(parts, "\n"), Project: w.ProjectKey}, nil
}
