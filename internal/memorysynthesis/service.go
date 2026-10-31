package memorysynthesis

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// RunStore is the persistence the coordinator needs; *jobstore.Store implements it.
type RunStore interface {
	BeginMemorySynthesisIfChanged(jobstore.MemorySynthesisBegin) (jobstore.MemorySynthesisRun, jobstore.MemorySynthesisBeginOutcome, error)
	FinishMemorySynthesisRun(id string, f jobstore.MemorySynthesisFinish) (jobstore.MemorySynthesisRun, error)
}

// SynthResult is what a synthesizer hands back. P1 never calls a model: the function is a
// seam that tests (and later phases) fill in.
type SynthResult struct {
	Agent      string
	Model      string
	ResultJSON string
}

// Synthesizer turns prepared (already scrubbed and bounded) materials into a result.
type Synthesizer func(context.Context, Materials) (SynthResult, error)

// Outcome states reported by Service.Run.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	// OutcomeReused: a succeeded run with the same input digest exists; only last_checked_at moved.
	OutcomeReused = "reused"
	// OutcomeRunning: another run of the same project and day is in flight.
	OutcomeRunning = "running"
	// OutcomeEmpty: the window has no material, so no record was created.
	OutcomeEmpty = "empty"
)

// Outcome is the result of one Service.Run call. Run is zero for OutcomeEmpty.
type Outcome struct {
	State string
	Run   jobstore.MemorySynthesisRun
}

// Service coordinates material preparation and the run record. It owns no goroutines;
// callers (an explicit run command, later a scheduler) decide when to call Run.
type Service struct {
	Store   RunStore
	Sources []Source
	Options Options
	Synth   Synthesizer
	// Loc is the server time zone used for the business-day window (default time.Local).
	Loc      *time.Location
	Lookback time.Duration
	// Timeout bounds a run; a running record older than this is treated as stale.
	Timeout time.Duration
	// KeepSnapshot stores the scrubbed envelope on the run record (keep_material_snapshot).
	KeepSnapshot bool
	Now          func() time.Time
	NewID        func() string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ms-%016x", time.Now().UnixNano())
	}
	return "ms-" + hex.EncodeToString(b[:])
}

// Prepare collects, scrubs and bounds the materials of one project day without touching the store.
func (s *Service) Prepare(ctx context.Context, project, day string) (Materials, error) {
	w, err := DayWindow(day, s.Loc, s.Lookback)
	if err != nil {
		return Materials{}, err
	}
	items, err := Collect(ctx, project, day, s.Sources...)
	if err != nil {
		return Materials{}, err
	}
	return Build(project, day, w.Timezone(), items, s.Options)
}

// Run prepares the materials and drives one run record: unchanged input reuses the existing
// succeeded record, a live run for the same day is left alone, and otherwise the synthesizer
// runs once and the record is finished as succeeded or failed.
func (s *Service) Run(ctx context.Context, project, day, createdBy string) (Outcome, error) {
	if s.Store == nil || s.Synth == nil {
		return Outcome{}, errors.New("memorysynthesis: store and synthesizer are required")
	}
	m, err := s.Prepare(ctx, project, day)
	if err != nil {
		return Outcome{}, err
	}
	if len(m.Items) == 0 {
		return Outcome{State: OutcomeEmpty}, nil
	}
	counts := map[string]int{}
	for _, it := range m.Items {
		counts[it.Kind]++
	}
	countsJSON, _ := json.Marshal(counts) // map[string]int marshals deterministically (sorted keys)
	omittedJSON, _ := json.Marshal(m.Omitted)
	in := jobstore.MemorySynthesisBegin{
		ID: s.newID(), ProjectKey: project, Day: day, InputDigest: m.InputDigest, MaterialSchema: m.Schema,
		MaterialCountsJSON: string(countsJSON), OmittedJSON: string(omittedJSON), CreatedBy: createdBy, Now: s.now().Unix(),
	}
	if s.Timeout > 0 {
		in.StaleBefore = s.now().Add(-s.Timeout).Unix()
	}
	if s.KeepSnapshot {
		// m is already scrubbed and bounded; the snapshot is exactly what a synthesizer would see.
		b, _ := json.Marshal(m)
		in.MaterialSnapshot = string(b)
	}
	run, begun, err := s.Store.BeginMemorySynthesisIfChanged(in)
	if err != nil {
		return Outcome{}, err
	}
	switch begun {
	case jobstore.MemorySynthesisReused:
		return Outcome{State: OutcomeReused, Run: run}, nil
	case jobstore.MemorySynthesisAlreadyRunning:
		return Outcome{State: OutcomeRunning, Run: run}, nil
	}
	res, synthErr := s.runSynth(ctx, m)
	fin := jobstore.MemorySynthesisFinish{Now: s.now().Unix(), Status: jobstore.MemorySynthesisSucceeded,
		Agent: res.Agent, Model: res.Model, ResultJSON: res.ResultJSON}
	if synthErr == nil && res.ResultJSON != "" {
		sum := sha256.Sum256([]byte(res.ResultJSON))
		fin.OutputDigest = hex.EncodeToString(sum[:])
	}
	if synthErr != nil {
		msg, _ := Scrub(synthErr.Error())
		fin.Status, fin.Error, fin.ResultJSON = jobstore.MemorySynthesisFailed, trimRunes(msg, 500), ""
	}
	done, err := s.Store.FinishMemorySynthesisRun(run.ID, fin)
	if err != nil {
		return Outcome{}, err
	}
	if synthErr != nil {
		return Outcome{State: OutcomeFailed, Run: done}, nil
	}
	return Outcome{State: OutcomeSucceeded, Run: done}, nil
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	return newRunID()
}

func (s *Service) runSynth(ctx context.Context, m Materials) (res SynthResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("synthesizer panic: %v", r)
		}
	}()
	return s.Synth(ctx, m)
}
