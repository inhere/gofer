package steward

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// The steward notes are a versioned Markdown document riding on plan_handoffs (same
// storage, same optimistic lock; the key is the reserved namespace jobstore.StewardNotesKey).
// Every write appends a new version, so a rewrite — the steward slimming the notes down,
// or a person editing them — never loses the previous text.

// Errors of the notes API.
var (
	ErrNotesConflict = errors.New("steward notes changed since you read them; read the latest version and retry")
	ErrNotesTooLarge = fmt.Errorf("steward notes exceed %d KiB", jobstore.StewardNotesMaxBytes>>10)
)

// Notes is one version of the steward notes.
type Notes struct {
	Version int    `json:"version"`
	Body    string `json:"body"`
	By      string `json:"by,omitempty"`
	At      int64  `json:"at,omitempty"`
}

// NotesInfo summarises the latest version.
type NotesInfo struct {
	Version int `json:"version"`
	Bytes   int `json:"bytes"`
	// NeedSlim is true when the notes outgrew the soft cap (8KB): the next review asks the
	// steward to rewrite them shorter.
	NeedSlim bool `json:"need_slim"`
}

func toNotes(h jobstore.PlanHandoff) Notes {
	return Notes{Version: h.Version, Body: h.Body, By: h.By, At: h.At}
}

// GetNotes returns the latest notes (version 0 = none written yet) or, with version > 0,
// that historical version (ok=false when it does not exist).
func (s *Service) GetNotes(version int) (Notes, bool, error) {
	h, ok, err := s.store.GetPlanHandoff(jobstore.StewardNotesKey, version)
	if err != nil {
		return Notes{}, false, err
	}
	if !ok {
		if version > 0 {
			return Notes{}, false, nil
		}
		return Notes{}, true, nil
	}
	return toNotes(h), true, nil
}

// NotesHistory lists every version, newest first (bodies included: they are small).
func (s *Service) NotesHistory() ([]Notes, error) {
	hs, err := s.store.ListPlanHandoffHistory(jobstore.StewardNotesKey)
	if err != nil {
		return nil, err
	}
	out := make([]Notes, 0, len(hs))
	for _, h := range hs {
		out = append(out, toNotes(h))
	}
	return out, nil
}

// NotesStatus reports the latest version, its size and whether it needs slimming.
func (s *Service) NotesStatus() (NotesInfo, error) {
	n, _, err := s.GetNotes(0)
	if err != nil {
		return NotesInfo{}, err
	}
	return NotesInfo{Version: n.Version, Bytes: len(n.Body), NeedSlim: len(n.Body) > config.StewardNotesMaxBytes}, nil
}

// SetNotes writes a new version if expectedVersion is the current one (0 = the first
// write). The previous versions stay.
func (s *Service) SetNotes(body, by string, expectedVersion int) (Notes, error) {
	h, err := s.store.SetPlanHandoff(jobstore.StewardNotesKey, body, by, expectedVersion)
	switch {
	case errors.Is(err, jobstore.ErrPlanHandoffConflict):
		return Notes{}, fmt.Errorf("%w (%v)", ErrNotesConflict, strings.TrimPrefix(err.Error(), jobstore.ErrPlanHandoffConflict.Error()+": "))
	case errors.Is(err, jobstore.ErrPlanHandoffTooLarge):
		return Notes{}, ErrNotesTooLarge
	case err != nil:
		return Notes{}, err
	}
	return toNotes(h), nil
}
