package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/seat"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// The seat roster routes (design 2026-11-18 resident-sessions §4): /v1/seats. The business
// lives in internal/seat; this file binds requests, applies the permission and ceiling rules
// and maps the service's errors to statuses.
//
// Who is asking: a CLI inside a terminal session reaches the server with the USER's token plus
// the X-Gofer-Session-Id header (client.applyAuth); there is no separate session credential.
// A write that names a registered, live session of the same owner is therefore recorded as
// `session:<sid>` (seatWriter), everything else as `user`.

// seatHolder builds the seat service once, on first use, from what the server already holds.
type seatHolder struct {
	once sync.Once
	svc  *seat.Service
}

// seats returns the seat service; nil when this server has no metadata store.
func (s *Server) seats() *seat.Service {
	s.seatSvc.once.Do(func() {
		if s.jobs == nil || s.jobs.Meta() == nil {
			return
		}
		d := seat.Deps{
			Projects:  seatProjects{s},
			Waiting:   seatWaiting{s},
			Restarter: seatRestarter{jobs: s.jobs, agents: s.agents},
		}
		if s.agents != nil {
			d.Agents = s.agents
		}
		if s.relay != nil {
			d.Messenger = s.relay
		}
		d.Sayer = s.jobs
		s.seatSvc.svc = seat.New(s.jobs.Meta(), d)
	})
	return s.seatSvc.svc
}

// seatScheduleDeliverer is the seat service as the job package's SeatDeliverer: the seat
// service is built on first use, so the lookup happens per delivery.
type seatScheduleDeliverer struct{ s *Server }

func (d seatScheduleDeliverer) DeliverToSeat(ctx context.Context, req job.SeatDeliverRequest) (job.SeatDeliverResult, error) {
	svc := d.s.seats()
	if svc == nil {
		return job.SeatDeliverResult{}, job.ErrSeatDeliveryUnavailable
	}
	return svc.DeliverToSeat(ctx, req)
}

// seatProjects adapts the project registry to seat.Projects.
type seatProjects struct{ s *Server }

func (p seatProjects) Paths(key string) (string, string, bool) {
	if p.s.projects == nil {
		return "", "", false
	}
	cfg := p.s.projects.Config()
	if cfg == nil {
		return "", "", false
	}
	proj, ok := cfg.Projects[key]
	if !ok {
		return "", "", false
	}
	return proj.HostPath, proj.ContainerPath, true
}

// seatRestarter starts the replacement session job of an ACP seat. The conversation is
// continued through the restart rule the mailbox's resident agents use (job.StampSessionResume).
type seatRestarter struct {
	jobs   *job.Service
	agents *agent.Registry
}

// seatSessionIdleSec is how long a restarted seat session may sit idle before it ends.
const seatSessionIdleSec = 3600

// seatTurnTimeoutSec bounds one turn of a restarted seat session.
const seatTurnTimeoutSec = 30 * 60

func (r seatRestarter) RestartACP(_ context.Context, q seat.RestartRequest) (string, error) {
	runner := strings.TrimSpace(q.Runner)
	if runner == "" {
		runner = config.BuiltinLocalRunner
	}
	cwd := q.RelCwd
	if cwd == "" {
		cwd = "."
	}
	shared := false
	req := job.JobRequest{
		ProjectKey: q.ProjectKey, Agent: q.Agent, Runner: config.NormalizeRunnerName(runner), Cwd: cwd,
		Prompt: q.Prompt, Title: "seat session", Session: true,
		IdleTimeoutSec: seatSessionIdleSec, TimeoutSec: seatTurnTimeoutSec,
		// A seat session lives for hours: it must not hold the project directory lock.
		ExclusiveDir: &shared,
		CallerID:     q.CallerID, OriginCaller: q.CallerID,
	}
	job.StampSessionResume(&req, job.CanLoadSession(r.agents, q.Agent), q.SessionID, q.FromJobID)
	res, err := r.jobs.Submit(req)
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

// seatProjectsOf is the resource resolver of Resource("seat"): the project of a seat.
func seatProjectsOf(s *Server, id string) ([]string, bool, error) {
	svc := s.seats()
	if svc == nil || id == "" {
		return nil, false, nil
	}
	x, err := svc.Get(id)
	if errors.Is(err, jobstore.ErrSeatNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []string{x.ProjectKey}, true, nil
}

// seatView is the wire form of a seat plus its derived status.
type seatView struct {
	ID         string `json:"id"`
	CallerID   string `json:"caller_id,omitempty"`
	ProjectKey string `json:"project_key"`
	Name       string `json:"name"`
	RelCwd     string `json:"rel_cwd,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	Charter    string `json:"charter,omitempty"`
	// CharterBy is "user" or "session:<sid>": who last changed purpose / charter.
	CharterBy  string `json:"charter_by"`
	Kind       string `json:"kind"`
	Agent      string `json:"agent,omitempty"`
	CurrentRef string `json:"current_ref,omitempty"`
	ArchivedAt int64  `json:"archived_at,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
	// State is the sitting session's state (none / running / awaiting / offline / ended);
	// Waiting is the "waiting for me" category, Unread whether it was active since the viewer
	// last looked, ThreadID its workbench thread.
	State      string `json:"state"`
	Waiting    string `json:"waiting,omitempty"`
	Unread     bool   `json:"unread,omitempty"`
	ActivityAt int64  `json:"activity_at,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	// CharterSent / CharterError describe the one message a bind sends: an acp seat whose job was
	// live when it was bound said the seat's charter into it right away. They ride the bind answer
	// only (omitted everywhere else), and a failed send never fails the bind.
	CharterSent  bool   `json:"charter_sent,omitempty"`
	CharterError string `json:"charter_error,omitempty"`
}

func seatViewOf(st seat.Status) seatView {
	x := st.Seat
	return seatView{
		ID: x.ID, CallerID: x.CallerID, ProjectKey: x.ProjectKey, Name: x.Name, RelCwd: x.RelCwd,
		Purpose: x.Purpose, Charter: x.Charter, CharterBy: x.CharterBy, Kind: x.Kind, Agent: x.Agent,
		CurrentRef: x.CurrentRef, ArchivedAt: x.ArchivedAt, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt,
		State: st.State, Waiting: st.Waiting, Unread: st.Unread, ActivityAt: st.ActivityAt, ThreadID: st.ThreadID,
	}
}

// sessionSeatView is the `seat` object of a session view: only present when the session sits
// in a seat. It carries what the session's hook needs to inject the seat block — the name, the
// purpose and the charter (or the repository-relative path it lives in, which the hook reads
// itself so the file's current content wins) — and, on the registration answer only, the
// one-time continuation text.
type sessionSeatView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Purpose string `json:"purpose,omitempty"`
	// Charter is the charter text cut to seat.CharterBudget; empty when CharterPath is set.
	Charter string `json:"charter,omitempty"`
	// CharterPath is the repository-relative path of the charter file (the seat's charter was
	// written as "@<path>").
	CharterPath string `json:"charter_path,omitempty"`
	// Root is the project root CharterPath is relative to, in the spelling of this session's cwd
	// (a session inside a container gets the container path).
	Root string `json:"root,omitempty"`
	// Continuation is the text a session that has just taken the seat must see once; it rides
	// the registration answer and is marked delivered in the same request, so later answers
	// leave it empty.
	Continuation string `json:"continuation,omitempty"`
}

// seatOfSession returns the seat a session sits in for its view; nil when none. cwd picks the
// project root spelling a charter path is resolved against.
func (s *Server) seatOfSession(sid, cwd string) *sessionSeatView {
	return s.seatInOf(nil, sid, cwd)
}

// seatInOf is seatOfSession against a prefetched seat map: a non-nil seats answers from it (no
// query), a nil one falls back to the per-session lookup.
func (s *Server) seatInOf(seats map[string]jobstore.Seat, sid, cwd string) *sessionSeatView {
	if seats != nil {
		x, ok := seats[sid]
		if !ok {
			return nil
		}
		return sessionSeatViewOf(x, cwd, s.projectRoots)
	}
	svc := s.seats()
	if svc == nil {
		return nil
	}
	x, ok, err := svc.SeatOfRef(sid)
	if err != nil || !ok {
		return nil
	}
	return sessionSeatViewOf(x, cwd, s.projectRoots)
}

// sessionSeatViewOf renders the `seat` object of a session view from the seat row. cwd picks the
// project root spelling a charter path is resolved against.
func sessionSeatViewOf(x jobstore.Seat, cwd string, roots func(string) []string) *sessionSeatView {
	v := &sessionSeatView{ID: x.ID, Name: x.Name, Purpose: x.Purpose}
	if p, text := seat.SplitCharter(x.Charter); p != "" {
		v.CharterPath, v.Root = p, seatRootFor(roots(x.ProjectKey), cwd)
	} else {
		v.Charter = seat.Cut(strings.TrimSpace(text), seat.CharterBudget)
	}
	return v
}

// seatRootFor picks the project root a session's relative charter path is joined to: the root
// the session's cwd sits under (host or container spelling), else the first one.
func seatRootFor(roots []string, cwd string) string {
	cwd = strings.ReplaceAll(cwd, "\\", "/")
	best := ""
	for _, r := range roots {
		r = strings.TrimRight(strings.ReplaceAll(r, "\\", "/"), "/")
		if r == "" {
			continue
		}
		if cwd == r || strings.HasPrefix(cwd, r+"/") {
			if len(r) > len(best) {
				best = r
			}
		}
	}
	if best != "" {
		return best
	}
	for _, r := range roots {
		if strings.TrimSpace(r) != "" {
			return r
		}
	}
	return ""
}

// maySeat is the owner rule of the seat routes: a seat's owner needs nothing, anyone else needs
// seat.manage.others. Only a user-class caller owns seats.
func (s *Server) maySeat(c *rux.Context, owner string) bool {
	if callerKindFromCtx(c) != callerKindUser {
		return false
	}
	if owner == "" || owner == callerFromCtx(c) {
		return true
	}
	return s.can(c, authz.PermSeatManageOthers)
}

// seatWriter says who is writing: "session:<sid>" when the request names a registered, live
// session of the same owner through X-Gofer-Session-Id, "user" otherwise.
func (s *Server) seatWriter(c *rux.Context) string {
	sid := requestSessionID(c)
	if sid == "" || s.jobs == nil || s.jobs.Meta() == nil {
		return jobstore.SeatByUser
	}
	anchor, found, err := s.jobs.Meta().SessionGrantAnchor(sid)
	if err != nil || !found || anchor.Ended() {
		return jobstore.SeatByUser
	}
	if caller := callerFromCtx(c); anchor.Owner != "" && anchor.Owner != caller {
		return jobstore.SeatByUser
	}
	return jobstore.SeatBySessionPrefix + sid
}

func seatStatus(err error) int {
	switch {
	case errors.Is(err, jobstore.ErrSeatNotFound), errors.Is(err, sessionrelay.ErrUnknownSession):
		return http.StatusNotFound
	case errors.Is(err, jobstore.ErrSeatNameTaken), errors.Is(err, jobstore.ErrSeatOccupied),
		errors.Is(err, jobstore.ErrSeatRefTaken), errors.Is(err, jobstore.ErrSeatArchived):
		return http.StatusConflict
	case errors.Is(err, jobstore.ErrSeatInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) seatsReady(c *rux.Context) (*seat.Service, bool) {
	if callerKindFromCtx(c) != callerKindUser {
		writeError(c, http.StatusForbidden, "seats require a user caller", "worker and job credentials cannot use the seat roster")
		return nil, false
	}
	svc := s.seats()
	if svc == nil {
		writeError(c, http.StatusServiceUnavailable, "seats unavailable", "no job store wired on this server")
		return nil, false
	}
	return svc, true
}

// loadSeat reads the seat named by the path and applies the owner rule.
func (s *Server) loadSeat(c *rux.Context) (*seat.Service, jobstore.Seat, bool) {
	svc, ok := s.seatsReady(c)
	if !ok {
		return nil, jobstore.Seat{}, false
	}
	x, err := svc.Get(c.Param("id"))
	if err != nil {
		writeError(c, seatStatus(err), "seat lookup failed", err.Error())
		return nil, jobstore.Seat{}, false
	}
	if !s.maySeat(c, x.CallerID) {
		s.writePermissionDenied(c, authz.PermSeatManageOthers)
		return nil, jobstore.Seat{}, false
	}
	return svc, x, true
}

func (s *Server) writeSeat(c *rux.Context, svc *seat.Service, x jobstore.Seat) {
	st, err := svc.Status(callerFromCtx(c), x)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "seat status failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, seatViewOf(st))
}

// GET /v1/seats: the caller's own seats; all=1 adds everyone's for a holder of
// seat.manage.others (without the permission the list stays the caller's own). Seats outside
// the caller's project ceiling are left out.
func (s *Server) handleSeatList(c *rux.Context) {
	svc, ok := s.seatsReady(c)
	if !ok {
		return
	}
	f := jobstore.SeatFilter{CallerID: callerFromCtx(c), ProjectKey: strings.TrimSpace(c.Query("project")),
		IncludeArchived: c.Query("include_archived") == "1"}
	if c.Query("all") == "1" && s.can(c, authz.PermSeatManageOthers) {
		f.CallerID = ""
	}
	rows, err := svc.List(f)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "list seats failed", err.Error())
		return
	}
	rows = scopeRows(s, c, rows, func(x jobstore.Seat) string { return x.ProjectKey })
	sts, err := svc.Statuses(callerFromCtx(c), rows)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "seat status failed", err.Error())
		return
	}
	out := make([]seatView, 0, len(sts))
	for _, st := range sts {
		out = append(out, seatViewOf(st))
	}
	c.JSON(http.StatusOK, map[string]any{"seats": out})
}

type seatCreateReq struct {
	ProjectKey string `json:"project_key"`
	Name       string `json:"name"`
	RelCwd     string `json:"rel_cwd"`
	Purpose    string `json:"purpose"`
	Charter    string `json:"charter"`
	Kind       string `json:"kind"`
	Agent      string `json:"agent"`
}

// POST /v1/seats creates a vacant seat for the caller.
func (s *Server) handleSeatCreate(c *rux.Context) {
	svc, ok := s.seatsReady(c)
	if !ok {
		return
	}
	var body seatCreateReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if !s.requireProjectScope(c, strings.TrimSpace(body.ProjectKey)) {
		return
	}
	x, err := svc.Create(seat.CreateInput{
		CallerID: callerFromCtx(c), ProjectKey: strings.TrimSpace(body.ProjectKey), Name: body.Name, RelCwd: body.RelCwd,
		Purpose: body.Purpose, Charter: body.Charter, CharterBy: s.seatWriter(c), Kind: body.Kind, Agent: body.Agent,
	})
	if err != nil {
		writeError(c, seatStatus(err), "create seat failed", err.Error())
		return
	}
	s.writeSeat(c, svc, x)
}

// GET /v1/seats/{id}
func (s *Server) handleSeatGet(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	s.writeSeat(c, svc, x)
}

type seatPatchReq struct {
	Name     *string `json:"name"`
	RelCwd   *string `json:"rel_cwd"`
	Purpose  *string `json:"purpose"`
	Charter  *string `json:"charter"`
	Agent    *string `json:"agent"`
	Archived *bool   `json:"archived"`
}

// PATCH /v1/seats/{id}. A change of purpose / charter records who made it (charter_by) and
// appends to the charter history; "archived": true archives the seat.
func (s *Server) handleSeatPatch(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	var body seatPatchReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	by := s.seatWriter(c)
	if body.Name != nil || body.RelCwd != nil || body.Purpose != nil || body.Charter != nil || body.Agent != nil {
		var err error
		x, err = svc.Update(x.ID, jobstore.SeatPatch{Name: body.Name, RelCwd: body.RelCwd, Purpose: body.Purpose,
			Charter: body.Charter, Agent: body.Agent, By: by})
		if err != nil {
			writeError(c, seatStatus(err), "update seat failed", err.Error())
			return
		}
	}
	if body.Archived != nil && *body.Archived {
		var err error
		if x, err = svc.Archive(x.ID, by); err != nil {
			writeError(c, seatStatus(err), "archive seat failed", err.Error())
			return
		}
	}
	s.writeSeat(c, svc, x)
}

// DELETE /v1/seats/{id} removes the seat and its history.
func (s *Server) handleSeatDelete(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	if err := svc.Delete(x.ID); err != nil {
		writeError(c, seatStatus(err), "delete seat failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"deleted": x.ID})
}

type seatBindReq struct {
	// Ref is the terminal session id (kind terminal) or the ACP job id (kind acp).
	Ref string `json:"ref"`
	// Replace lets the bind take over a seat that already has another session.
	Replace bool `json:"replace"`
}

// POST /v1/seats/{id}/bind
func (s *Server) handleSeatBind(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	var body seatBindReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	got, info, err := svc.BindInfo(x.ID, body.Ref, body.Replace, s.seatWriter(c))
	if err != nil {
		writeError(c, seatStatus(err), "bind seat failed", err.Error())
		return
	}
	st, err := svc.Status(callerFromCtx(c), got)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "seat status failed", err.Error())
		return
	}
	v := seatViewOf(st)
	v.CharterSent = info.CharterSent
	if info.CharterError != nil {
		v.CharterError = info.CharterError.Error()
	}
	c.JSON(http.StatusOK, v)
}

// POST /v1/seats/{id}/unbind
func (s *Server) handleSeatUnbind(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	got, err := svc.Unbind(x.ID, s.seatWriter(c))
	if err != nil {
		writeError(c, seatStatus(err), "unbind seat failed", err.Error())
		return
	}
	s.writeSeat(c, svc, got)
}

// seatOpenPlanView is the wire form of seat.OpenPlan.
type seatOpenPlanView struct {
	SeatID        string `json:"seat_id"`
	Kind          string `json:"kind"`
	Runner        string `json:"runner,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	Resumable     bool   `json:"resumable"`
	Alive         bool   `json:"alive,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Sh            string `json:"sh,omitempty"`
	PowerShell    string `json:"powershell,omitempty"`
	NewSh         string `json:"new_sh,omitempty"`
	NewPowerShell string `json:"new_powershell,omitempty"`
	ThreadID      string `json:"thread_id,omitempty"`
	NeedsRestart  bool   `json:"needs_restart,omitempty"`
	Restarted     bool   `json:"restarted,omitempty"`
	JobID         string `json:"job_id,omitempty"`
}

// GET /v1/seats/{id}/open-plan: how to get back into the seat. Read only: an ended ACP
// session is reported as needs_restart, starting it again is POST /v1/seats/{id}/restart.
func (s *Server) handleSeatOpenPlan(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	p, err := svc.OpenPlan(x.ID)
	if err != nil {
		writeError(c, seatStatus(err), "seat open plan failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, openPlanView(p))
}

// seatHistoryView is the wire form of a seat's history: every binding that was made and every
// revision of its purpose / charter, oldest first.
type seatHistoryView struct {
	Bindings []seatBindingView `json:"bindings"`
	Charter  []seatCharterView `json:"charter"`
}

// seatBindingView is one history row of who sat in the seat (action is bind / unbind / archive
// and friends, from_ref / to_ref the session or job id on either side).
type seatBindingView struct {
	ID      int64  `json:"id"`
	Action  string `json:"action"`
	FromRef string `json:"from_ref,omitempty"`
	ToRef   string `json:"to_ref,omitempty"`
	By      string `json:"by"`
	At      int64  `json:"at"`
}

// seatCharterView is one history row of a seat's purpose / charter.
type seatCharterView struct {
	ID      int64  `json:"id"`
	Purpose string `json:"purpose,omitempty"`
	Charter string `json:"charter,omitempty"`
	By      string `json:"by"`
	At      int64  `json:"at"`
}

// GET /v1/seats/{id}/history: the binding and charter history of a seat. Read only, and the
// owner rule is the one of GET /v1/seats/{id}.
func (s *Server) handleSeatHistory(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	bindings, charter, err := svc.History(x.ID)
	if err != nil {
		writeError(c, seatStatus(err), "seat history failed", err.Error())
		return
	}
	out := seatHistoryView{
		Bindings: make([]seatBindingView, 0, len(bindings)),
		Charter:  make([]seatCharterView, 0, len(charter)),
	}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, seatBindingView{ID: b.ID, Action: b.Action, FromRef: b.FromRef, ToRef: b.ToRef, By: b.By, At: b.At})
	}
	for _, h := range charter {
		out.Charter = append(out.Charter, seatCharterView{ID: h.ID, Purpose: h.Purpose, Charter: h.Charter, By: h.By, At: h.At})
	}
	c.JSON(http.StatusOK, out)
}

// GET /v1/sessions/{sid}/seat-candidates: the seats a terminal session could be bound to.
func (s *Server) handleSeatCandidates(c *rux.Context) {
	svc, ok := s.seatsReady(c)
	if !ok {
		return
	}
	sid := strings.TrimSpace(c.Param("sid"))
	a, found, err := s.jobs.Meta().GetAgentSession(sid)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read session failed", err.Error())
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, "unknown session", sid)
		return
	}
	if !s.maySeat(c, a.CallerID) {
		s.writePermissionDenied(c, authz.PermSeatManageOthers)
		return
	}
	rows, err := svc.Candidates(sid)
	if err != nil {
		writeError(c, seatStatus(err), "seat candidates failed", err.Error())
		return
	}
	sts, err := svc.Statuses(callerFromCtx(c), rows)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "seat status failed", err.Error())
		return
	}
	out := make([]seatView, 0, len(sts))
	for _, st := range sts {
		out = append(out, seatViewOf(st))
	}
	c.JSON(http.StatusOK, map[string]any{"seats": out})
}

func openPlanView(p seat.OpenPlan) seatOpenPlanView {
	return seatOpenPlanView{
		SeatID: p.SeatID, Kind: p.Kind, Runner: p.Runner, Cwd: p.Cwd, Resumable: p.Resumable, Alive: p.Alive,
		Reason: p.Reason, Sh: p.Sh, PowerShell: p.PowerShell, NewSh: p.NewSh, NewPowerShell: p.NewPowerShell,
		ThreadID: p.ThreadID, NeedsRestart: p.NeedsRestart, Restarted: p.Restarted, JobID: p.JobID,
	}
}

// POST /v1/seats/{id}/restart starts the ended ACP session of an acp seat again from its
// original session id and moves the seat to the new job. It submits a job, so the route needs
// job.submit (the gate enforces it, session grants included) on top of owning the seat; a seat
// that has no ended session to restart is 400.
func (s *Server) handleSeatRestart(c *rux.Context) {
	svc, x, ok := s.loadSeat(c)
	if !ok {
		return
	}
	p, err := svc.Restart(c.Req.Context(), x.ID)
	if err != nil {
		writeError(c, seatStatus(err), "restart seat failed", err.Error())
		return
	}
	c.JSON(http.StatusOK, openPlanView(p))
}
