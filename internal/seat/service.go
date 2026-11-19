// Package seat is the business layer of the seat roster (design
// docs/design/2026-11-18-resident-sessions-and-team-design.md §4): a seat is a long-lived
// working position of a caller in a project, and sessions come and go while the seat stays.
//
// The rows live in jobstore (seats / seat_bindings / seat_charter_history). This package adds
// what is derived from them: validation, binding candidates, the status of the sitting session,
// the commands that open a seat, and the text that tells a fresh session what the seat is.
// Whether a session is "waiting for me" is NOT decided here: it comes in through WaitingLookup.
package seat

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// WaitingLookup answers which of a caller's sessions are waiting for that caller. refs use the
// seat convention: a terminal session is its session id, an ACP session is its job id. The
// result maps each waiting ref to its category (a non-empty string); a ref that is not waiting
// is absent.
type WaitingLookup interface {
	WaitingSubjects(caller string, refs []string) (map[string]string, error)
}

// Projects resolves a project's root paths on both sides (host / container).
type Projects interface {
	Paths(projectKey string) (hostPath, containerPath string, ok bool)
}

// Agents resolves an agent definition by key (*agent.Registry satisfies it).
type Agents interface {
	Get(key string) (config.AgentConfig, bool)
}

// RestartRequest asks the host to start an ACP session job again for a seat whose current job
// has ended, continuing the conversation SessionID of FromJobID when the agent can load it.
type RestartRequest struct {
	ProjectKey string
	Agent      string
	Runner     string
	// RelCwd is the project-relative directory.
	RelCwd    string
	SessionID string
	FromJobID string
	CallerID  string
	// Prompt is the opening input of the new job.
	Prompt string
}

// ACPRestarter starts the replacement session job and returns its id.
type ACPRestarter interface {
	RestartACP(ctx context.Context, req RestartRequest) (jobID string, err error)
}

// Deps are the collaborators of a Service. Every field is optional: a missing one disables the
// feature that needs it (no Projects: paths are not checked and open plans have no directory).
type Deps struct {
	Projects  Projects
	Agents    Agents
	Waiting   WaitingLookup
	Restarter ACPRestarter
	// Lang is the language of the text injected into a session ("" = the default).
	Lang func() i18n.Lang
	Now  func() time.Time
}

// Service is the seat roster service.
type Service struct {
	store *jobstore.Store
	d     Deps
}

// New builds a Service over the metadata store.
func New(store *jobstore.Store, d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{store: store, d: d}
}

func (s *Service) lang() i18n.Lang {
	if s.d.Lang != nil {
		return s.d.Lang()
	}
	return i18n.Default
}

// CreateInput is what a new seat needs.
type CreateInput struct {
	CallerID   string
	ProjectKey string
	Name       string
	// RelCwd is the directory of the seat: project-relative, or an absolute path inside one of
	// the project's roots (host or container spelling), which is converted.
	RelCwd  string
	Purpose string
	Charter string
	// CharterBy is jobstore.SeatByUser (default) or "session:<sid>".
	CharterBy string
	Kind      string
	Agent     string
}

// Create adds a vacant seat.
func (s *Service) Create(in CreateInput) (jobstore.Seat, error) {
	rel, err := s.normalizeRel(in.ProjectKey, in.RelCwd)
	if err != nil {
		return jobstore.Seat{}, err
	}
	if s.d.Projects != nil {
		if _, _, ok := s.d.Projects.Paths(in.ProjectKey); !ok {
			return jobstore.Seat{}, fmt.Errorf("%w: unknown project %q", jobstore.ErrSeatInvalid, in.ProjectKey)
		}
	}
	return s.store.CreateSeat(jobstore.Seat{
		CallerID: in.CallerID, ProjectKey: in.ProjectKey, Name: in.Name, RelCwd: rel,
		Purpose: in.Purpose, Charter: in.Charter, CharterBy: in.CharterBy, Kind: in.Kind, Agent: in.Agent,
	})
}

// Get reads one seat.
func (s *Service) Get(id string) (jobstore.Seat, error) {
	x, ok, err := s.store.GetSeat(id)
	if err != nil {
		return jobstore.Seat{}, err
	}
	if !ok {
		return jobstore.Seat{}, jobstore.ErrSeatNotFound
	}
	return x, nil
}

// List lists seats.
func (s *Service) List(f jobstore.SeatFilter) ([]jobstore.Seat, error) { return s.store.ListSeats(f) }

// Update patches a seat; a RelCwd in the patch is normalized like Create's.
func (s *Service) Update(id string, p jobstore.SeatPatch) (jobstore.Seat, error) {
	if p.RelCwd != nil {
		cur, err := s.Get(id)
		if err != nil {
			return jobstore.Seat{}, err
		}
		rel, err := s.normalizeRel(cur.ProjectKey, *p.RelCwd)
		if err != nil {
			return jobstore.Seat{}, err
		}
		p.RelCwd = &rel
	}
	return s.store.UpdateSeat(id, p)
}

// Archive archives a seat (it frees the name and gives up the sitting session).
func (s *Service) Archive(id, by string) (jobstore.Seat, error) { return s.store.ArchiveSeat(id, by) }

// Delete removes a seat and its history.
func (s *Service) Delete(id string) error {
	ok, err := s.store.DeleteSeat(id)
	if err != nil {
		return err
	}
	if !ok {
		return jobstore.ErrSeatNotFound
	}
	return nil
}

// Bind seats ref (a terminal session id or an ACP job id) in the seat. The reference has to
// exist and belong to the seat's project and caller; a seat that already has another session
// is jobstore.ErrSeatOccupied unless replace is set.
func (s *Service) Bind(id, ref string, replace bool, by string) (jobstore.Seat, error) {
	x, err := s.Get(id)
	if err != nil {
		return jobstore.Seat{}, err
	}
	ref = strings.TrimSpace(ref)
	switch x.Kind {
	case jobstore.SeatKindACP:
		rec, ok, err := s.store.GetJob(ref)
		if err != nil {
			return jobstore.Seat{}, err
		}
		if !ok {
			return jobstore.Seat{}, fmt.Errorf("%w: job %q not found", jobstore.ErrSeatInvalid, ref)
		}
		if rec.ProjectKey != x.ProjectKey {
			return jobstore.Seat{}, fmt.Errorf("%w: job %q belongs to project %q, not %q", jobstore.ErrSeatInvalid, ref, rec.ProjectKey, x.ProjectKey)
		}
	default:
		a, ok, err := s.store.GetAgentSession(ref)
		if err != nil {
			return jobstore.Seat{}, err
		}
		if !ok {
			return jobstore.Seat{}, fmt.Errorf("%w: session %q not found", jobstore.ErrSeatInvalid, ref)
		}
		if a.ProjectKey != x.ProjectKey {
			return jobstore.Seat{}, fmt.Errorf("%w: session %q belongs to project %q, not %q", jobstore.ErrSeatInvalid, ref, a.ProjectKey, x.ProjectKey)
		}
		if a.CallerID != "" && x.CallerID != "" && a.CallerID != x.CallerID {
			return jobstore.Seat{}, fmt.Errorf("%w: session %q belongs to another caller", jobstore.ErrSeatInvalid, ref)
		}
	}
	return s.store.BindSeat(id, ref, replace, by)
}

// Unbind empties the seat.
func (s *Service) Unbind(id, by string) (jobstore.Seat, error) { return s.store.UnbindSeat(id, by) }

// History returns the binding and charter history of a seat.
func (s *Service) History(id string) ([]jobstore.SeatBinding, []jobstore.SeatCharterRevision, error) {
	b, err := s.store.ListSeatBindings(id)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.store.ListSeatCharterHistory(id)
	return b, c, err
}

// SeatOfRef returns the seat a session / job sits in (ok=false: none).
func (s *Service) SeatOfRef(ref string) (jobstore.Seat, bool, error) {
	m, err := s.store.SeatsByRefs([]string{ref})
	if err != nil {
		return jobstore.Seat{}, false, err
	}
	x, ok := m[ref]
	return x, ok, nil
}

// roots returns a project's root paths on both sides.
func (s *Service) roots(projectKey string) []string {
	if s.d.Projects == nil {
		return nil
	}
	h, c, ok := s.d.Projects.Paths(projectKey)
	if !ok {
		return nil
	}
	return []string{h, c}
}

// normalizeRel turns what a person typed for a seat's directory into the stored form: a clean
// project-relative slash path ("" = the project root). An absolute path is converted against
// the project's roots (either spelling); one outside them, or a relative one that climbs out,
// is refused.
func (s *Service) normalizeRel(projectKey, in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" || in == "." {
		return "", nil
	}
	rel, ok := sessionrelay.RelToRoots(in, s.roots(projectKey))
	if !ok {
		return "", fmt.Errorf("%w: directory %q is not inside project %q", jobstore.ErrSeatInvalid, in, projectKey)
	}
	rel = path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "." {
		return "", nil
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: directory %q leaves project %q", jobstore.ErrSeatInvalid, in, projectKey)
	}
	return rel, nil
}

// relUnder reports whether rel is seatRel itself or a directory below it ("" = the whole project).
func relUnder(rel, seatRel string) bool {
	if seatRel == "" {
		return true
	}
	return rel == seatRel || strings.HasPrefix(rel, seatRel+"/")
}
