// Package engine assembles one explicit daily memory synthesis run: it reads the live
// memory.synthesis configuration, builds the material sources and the model-backed
// synthesizer, and drives memorysynthesis.Service. It sits above memorysynthesis (and
// uses job for the one-shot model call) so the entry layers (httpapi, commands) only bind
// parameters and forward (G021); nothing in memorysynthesis imports it back (G022).
package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/memorysynthesis"
	"github.com/inhere/gofer/internal/memorysynthesis/sources"
)

// Errors a caller maps to an answer; the wrapped text says what to change.
var (
	// ErrDisabled: memory.synthesis.enabled is false.
	ErrDisabled = errors.New("memory synthesis is disabled: set memory.synthesis.enabled to true in the server configuration")
	// ErrProject: the project is unknown or not listed in memory.synthesis.projects.
	ErrProject = errors.New("memory synthesis project not available")
	// ErrAgent: the configured synthesis agent cannot run a read-only, tool-less call.
	ErrAgent = errors.New("memory synthesis agent unusable")
	// ErrDay: the day is not a YYYY-MM-DD date.
	ErrDay = errors.New("memory synthesis day must be YYYY-MM-DD")
)

// Deps are the engine's collaborators. Config returns the live configuration generation
// (hot reload takes effect on the next run).
type Deps struct {
	Store   *jobstore.Store
	Doctor  sources.DoctorReader
	Config  func() *config.Config
	OneShot OneShot
	Now     func() time.Time
}

// OneShot is memorysynthesis.OneShot plus the up-front usability check.
type OneShot interface {
	memorysynthesis.OneShot
	Check(agent string) error
}

// Engine runs explicit synthesis requests.
type Engine struct{ d Deps }

// New builds an Engine.
func New(d Deps) *Engine { return &Engine{d: d} }

// Request is one explicit run.
type Request struct {
	Project string
	// Day is YYYY-MM-DD in the server time zone; "" means today.
	Day       string
	CreatedBy string
	// Origin is the ceiling owner of the model call (the calling person).
	Origin string
	Force  bool
}

func (e *Engine) now() time.Time {
	if e.d.Now != nil {
		return e.d.Now()
	}
	return time.Now()
}

// Enabled reports whether memory.synthesis.enabled is on in the live configuration.
func (e *Engine) Enabled() bool {
	cfg := e.d.Config()
	return cfg != nil && cfg.Memory.Synthesis.Enabled
}

// Run executes one explicit synthesis: disabled configuration is refused before anything is
// read or sent; otherwise the run record, reuse and force semantics are those of
// memorysynthesis.Service.
func (e *Engine) Run(ctx context.Context, r Request) (memorysynthesis.Outcome, error) {
	cfg := e.d.Config()
	if cfg == nil || !cfg.Memory.Synthesis.Enabled {
		return memorysynthesis.Outcome{}, ErrDisabled
	}
	syn := cfg.Memory.Synthesis
	project := strings.TrimSpace(r.Project)
	if project == "" {
		return memorysynthesis.Outcome{}, fmt.Errorf("%w: project is required", ErrProject)
	}
	if _, ok := cfg.Projects[project]; !ok {
		return memorysynthesis.Outcome{}, fmt.Errorf("%w: unknown project %q", ErrProject, project)
	}
	if len(syn.Projects) > 0 && !slices.Contains(syn.Projects, project) {
		return memorysynthesis.Outcome{}, fmt.Errorf("%w: project %q is not listed in memory.synthesis.projects", ErrProject, project)
	}
	day := strings.TrimSpace(r.Day)
	if day == "" {
		day = e.now().In(time.Local).Format(memorysynthesis.DayLayout)
	} else if _, err := time.Parse(memorysynthesis.DayLayout, day); err != nil {
		return memorysynthesis.Outcome{}, ErrDay
	}
	agentKey := syn.EffectiveAgent(cfg.Work)
	if e.d.OneShot == nil {
		return memorysynthesis.Outcome{}, fmt.Errorf("%w: no model runner is wired", ErrAgent)
	}
	if err := e.d.OneShot.Check(agentKey); err != nil {
		return memorysynthesis.Outcome{}, fmt.Errorf("%w: %v", ErrAgent, err)
	}
	args := slices.Clone(syn.ModelArgs)
	if len(args) == 0 && agentKey == cfg.Work.SummarizerAgentName() {
		// Default to the work summarizer's cheap-model preset, but only for its own agent.
		args = cfg.Work.SummarizerArgsOrDefault()
	}
	scfg := sources.Config{Loc: time.Local, Lookback: syn.LookbackDuration()}
	var doctor memorysynthesis.Source
	if e.d.Doctor != nil {
		doctor = sources.Doctor{Config: scfg, Reader: e.d.Doctor}
	}
	srcs := []memorysynthesis.Source{
		sources.Work{Config: scfg, Store: e.d.Store},
		sources.Jobs{Config: scfg, Store: e.d.Store, ReportTail: sources.FileReportTail},
		sources.Plans{Config: scfg, Store: e.d.Store},
		sources.Previous{Config: scfg, Store: e.d.Store},
	}
	if doctor != nil {
		srcs = append(srcs, doctor)
	}
	svc := &memorysynthesis.Service{
		Store: e.d.Store, Sources: srcs, Options: memorysynthesis.Options{MaxInputBytes: syn.InputLimit()},
		Loc: time.Local, Lookback: syn.LookbackDuration(),
		// A run record older than the model timeout plus the job wait margin is stale.
		Timeout: syn.TimeoutDuration() + time.Minute, KeepSnapshot: syn.KeepMaterialSnapshot, Now: e.d.Now,
		Synth: memorysynthesis.NewSynthesizer(e.d.OneShot, memorysynthesis.SynthConfig{
			Agent: agentKey, Model: modelOf(args), Args: args, Timeout: syn.TimeoutDuration(),
			MaxOutputBytes: syn.OutputLimit(), Origin: r.Origin, Lang: cfg.LanguageFor(project),
		}),
	}
	return svc.RunWith(ctx, memorysynthesis.RunOptions{Project: project, Day: day, CreatedBy: r.CreatedBy, Force: r.Force})
}

// modelOf reads the model name out of the operator's argv (--model X / --model=X / -m X).
func modelOf(args []string) string {
	for i, a := range args {
		switch {
		case (a == "--model" || a == "-m") && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(a, "--model="):
			return strings.TrimPrefix(a, "--model=")
		}
	}
	return ""
}
