package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/team"
)

// The leader protocol's entry layer (plan 2026-11-26-team-plan.md §2.4): the engine lives in
// internal/team; this file builds it once, hands it to the job service (TeamNoter) and the
// mailbox (LeaderTurns), and binds POST /v1/teams/eval.

type protocolHolder struct {
	once sync.Once
	p    *team.Protocol
}

// leaderProtocol returns the leader protocol engine; nil when this server has no team service.
func (s *Server) leaderProtocol() *team.Protocol {
	s.proto.once.Do(func() {
		svc := s.teams()
		if svc == nil {
			return
		}
		meta := s.jobs.Meta()
		p := team.NewProtocol(meta, svc, team.NewRouter(meta))
		// s.mail is read at call time: serve may replace the mailbox (SetMail).
		p.OnChainEnd(func(ce team.ChainEnd) error {
			if m := s.mail; m != nil {
				return m.NoteChainEnd(ce)
			}
			return nil
		})
		s.proto.p = p
	})
	return s.proto.p
}

// lazyProtocol is the protocol engine as the job service's TeamNoter and the mailbox's
// LeaderTurns: the engine is built on first use, so the lookup happens per call, and a server
// with no team service answers "no team".
type lazyProtocol struct{ s *Server }

func (l lazyProtocol) TurnNote(lang i18n.Lang, ids ...string) string {
	if p := l.s.leaderProtocol(); p != nil {
		return p.TurnNote(lang, ids...)
	}
	return ""
}

func (l lazyProtocol) PeekNote(lang i18n.Lang, ids ...string) string {
	if p := l.s.leaderProtocol(); p != nil {
		return p.PeekNote(lang, ids...)
	}
	return ""
}

func (l lazyProtocol) TurnEnded(ids ...string) {
	if p := l.s.leaderProtocol(); p != nil {
		p.TurnEnded(ids...)
	}
}

func (l lazyProtocol) CheckSend(o team.Party) error {
	if p := l.s.leaderProtocol(); p != nil {
		return p.CheckSend(o)
	}
	return nil
}

func (l lazyProtocol) NoteSent(o team.Party) {
	if p := l.s.leaderProtocol(); p != nil {
		p.NoteSent(o)
	}
}

// leaderOrigin names who is calling as far as the leader protocol cares: the job behind a job
// credential, or the registered live terminal session the request names (the session header,
// checked against the caller). A person's token alone, a worker or anything else is not a party
// that can sit in a leader seat; whether it does is decided by the server's seat table.
func (s *Server) leaderOrigin(c *rux.Context) (team.Party, bool) {
	if jc, ok := jobCallerFromCtx(c); ok {
		return team.Party{Kind: jobstore.MailKindJob, ID: jc.JobID, Caller: callerFromCtx(c)}, true
	}
	if callerKindFromCtx(c) != callerKindUser {
		return team.Party{}, false
	}
	if w := s.seatWriter(c); strings.HasPrefix(w, jobstore.SeatBySessionPrefix) {
		return team.Party{Kind: jobstore.MailKindSession, ID: strings.TrimPrefix(w, jobstore.SeatBySessionPrefix), Caller: callerFromCtx(c)}, true
	}
	return team.Party{}, false
}

type teamEvalReq struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// POST /v1/teams/eval {outcome, reason}: the leader records how its turn went. Only the job
// credential of the job sitting in the leader seat, or the terminal session sitting in it, may
// (rule 8): a person's token, any other job and a member are refused with 403.
func (s *Server) handleTeamEval(c *rux.Context) {
	p := s.leaderProtocol()
	if p == nil {
		writeError(c, http.StatusServiceUnavailable, "teams unavailable", "no job store wired on this server")
		return
	}
	var body teamEvalReq
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	origin, ok := s.leaderOrigin(c)
	if !ok {
		writeError(c, http.StatusForbidden, "team eval denied", team.ErrNotLeader.Error())
		return
	}
	e, err := p.RecordEval(origin, strings.TrimSpace(body.Outcome), body.Reason)
	switch {
	case errors.Is(err, team.ErrNotLeader):
		writeError(c, http.StatusForbidden, "team eval denied", err.Error())
	case errors.Is(err, team.ErrIdleTurn):
		writeError(c, http.StatusConflict, "turn already idle", err.Error())
	case errors.Is(err, jobstore.ErrTeamInvalid):
		writeError(c, http.StatusBadRequest, "invalid team eval", err.Error())
	case err != nil:
		writeError(c, http.StatusInternalServerError, "record team eval failed", err.Error())
	default:
		c.JSON(http.StatusOK, teamEvalView{ID: e.ID, LeaderRef: e.LeaderRef, TurnRef: e.TurnRef, Outcome: e.Outcome, Reason: e.Reason, At: e.At})
	}
}
