package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/pushhub"
)

// Q2: the browser's single push connection. POST /v1/ws-ticket (authenticated, in the
// /v1 group) trades the bearer token for a 30s one-time ticket; GET /v1/ws?ticket=
// (registered OUTSIDE the auth group, like attach) consumes it, checks Origin, and
// upgrades. After the `hello` frame the client subscribes to topics (pushhub) and gets
// snapshots / increments / invalidations as JSON text frames.
const (
	wsTicketTTL    = 30 * time.Second
	wsPingEvery    = 20 * time.Second
	wsIdleTimeout  = 60 * time.Second
	wsWriteTimeout = 10 * time.Second
	wsReadLimit    = 16 * 1024

	wsCloseTooMany = websocket.StatusCode(4429)
	wsCloseIdle    = websocket.StatusCode(4408)
)

// Live returns the browser push hub so assembly can wire its sources (job store hook,
// event tap, worker presence, ...). It is never nil on a Server built by New.
func (s *Server) Live() *pushhub.Hub { return s.live }

// newPushHub builds the hub with the snapshot sources only the HTTP layer can compute
// (they reuse the REST handlers' own builders, so a push never disagrees with a GET).
func (s *Server) newPushHub() *pushhub.Hub {
	h := pushhub.New(pushhub.Options{
		Version: func() string { return s.build.DisplayVersion() },
	})
	h.SetSnapshot(pushhub.TopicStats, func() (any, error) {
		resp, serr := s.buildStats()
		if serr != nil {
			return nil, serr
		}
		return resp, nil
	})
	h.SetSnapshot(pushhub.TopicPending, s.pendingSnapshot)
	// A connection under a caller ceiling gets its own snapshot, computed for the projects its
	// ceiling admits (gofer-7798 第二期, plan decision 2). Without a scoped source a restricted
	// connection would get NO snapshot at all (pushhub fails closed rather than hand it the
	// unrestricted frame), which would leave the console's dashboard and bell empty.
	h.SetScopedSnapshot(pushhub.TopicStats, func(scope func(string) bool) (any, error) {
		resp, serr := s.buildStatsFor(scope)
		if serr != nil {
			return nil, serr
		}
		return resp, nil
	})
	h.SetScopedSnapshot(pushhub.TopicPending, func(scope func(string) bool) (any, error) {
		return s.pendingSnapshotFor(scope)
	})
	h.SetBackfill(func(jobID string, since int64) ([]pushhub.JobEvent, error) {
		if s.jobs == nil {
			return nil, nil
		}
		rows, err := s.jobs.ListJobEvents(jobID, since)
		if err != nil {
			return nil, err
		}
		out := make([]pushhub.JobEvent, 0, len(rows))
		for _, r := range rows {
			out = append(out, pushhub.JobEvent{Seq: r.Seq, Type: r.Type, Detail: r.Detail, At: r.At})
		}
		return out, nil
	})
	return h
}

// pendingApprovalLimit caps how many held jobs the bell's snapshot carries. A person
// deciding on held work sees them in 「今天」 / the board anyway; the snapshot only feeds
// the topbar toast.
const pendingApprovalLimit = 100

// approvalView is one job held for a person's approval in the pending snapshot
// (gofer-5wvc): what the toast needs (id, title, project, since when it waits, the hold
// deadline) plus the command the approval is ABOUT — deciding means reading it. The
// prompt, the request and the caller stay out of it.
//
// A job parked by a one-click update (paused_for_update) is a different status and can
// never appear here.
type approvalView struct {
	ID           string   `json:"id"`
	Title        string   `json:"title,omitempty"`
	ProjectKey   string   `json:"project_key"`
	WaitingSince int64    `json:"waiting_since"`
	ExpiresAt    int64    `json:"expires_at"`
	HoldCmd      []string `json:"hold_cmd,omitempty"`
}

// pendingSnapshot is the bell's data: what GET /v1/interactions,
// GET /v1/decisions?state=OPEN and the awaiting_approval jobs return, in one payload.
// (`approvals` rides along because a held job waits for the very person the bell is
// for: without it the browser only heard about it through the jobs topic — a Today
// refresh, no toast; gofer-5wvc.)
func (s *Server) pendingSnapshot() (any, error) {
	inter, err := s.jobs.ListPendingInteractions()
	if err != nil {
		return nil, err
	}
	decs, err := s.jobs.Meta().ListDecisions(jobstore.DecisionOpen, "")
	if err != nil {
		return nil, err
	}
	views := make([]decisionView, 0, len(decs))
	for _, d := range decs {
		views = append(views, toDecisionView(*d))
	}
	approvals, err := s.pendingApprovals()
	if err != nil {
		return nil, err
	}
	if inter == nil {
		inter = []job.Interaction{}
	}
	return map[string]any{"interactions": inter, "decisions": views, "approvals": approvals}, nil
}

// pendingSnapshotFor is the bell's data for a connection under a caller ceiling (gofer-7798
// 第二期): every list keeps only the rows of the projects the ceiling admits — the pending
// interactions and the held jobs by their job's project, the open decisions by their plan /
// session's (设计「聚合与计数不泄露」). A row nobody can attribute to a project is dropped
// (fail closed), so a restricted caller never learns of work outside its range.
func (s *Server) pendingSnapshotFor(scope func(string) bool) (any, error) {
	full, err := s.pendingSnapshot()
	if err != nil {
		return nil, err
	}
	payload := full.(map[string]any)
	inter := payload["interactions"].([]job.Interaction)
	keptInter := make([]job.Interaction, 0, len(inter))
	for _, it := range inter {
		if projects, ok, perr := jobProjects(s, it.JobID); perr == nil && ok && scopeAllowsProjects(scope, projects) {
			keptInter = append(keptInter, it)
		}
	}
	views := payload["decisions"].([]decisionView)
	keptDecs := make([]decisionView, 0, len(views))
	for _, v := range views {
		if projects, ok, perr := decisionProjects(s, v.ID); perr == nil && ok && scopeAllowsProjects(scope, projects) {
			keptDecs = append(keptDecs, v)
		}
	}
	approvals := payload["approvals"].([]approvalView)
	keptAppr := make([]approvalView, 0, len(approvals))
	for _, v := range approvals {
		if scope(v.ProjectKey) {
			keptAppr = append(keptAppr, v)
		}
	}
	return map[string]any{"interactions": keptInter, "decisions": keptDecs, "approvals": keptAppr}, nil
}

// scopeAllowsProjects reports whether every project of one record is inside the scope. A record
// that names no project is outside (the same fail-closed direction ceilingAllowsAll takes).
func scopeAllowsProjects(scope func(string) bool, projects []string) bool {
	if len(projects) == 0 {
		return false
	}
	for _, p := range projects {
		if p == "" || !scope(p) {
			return false
		}
	}
	return true
}

// pendingApprovals lists the jobs waiting in awaiting_approval, newest first (the order
// GET /v1/jobs already lists them in).
func (s *Server) pendingApprovals() ([]approvalView, error) {
	rows, err := s.jobs.ListJobs(job.ListOpts{Status: job.StatusAwaitingApproval, Limit: pendingApprovalLimit})
	if err != nil {
		return nil, err
	}
	out := make([]approvalView, 0, len(rows))
	for _, r := range rows {
		v := approvalView{ID: r.ID, Title: r.Title, ProjectKey: r.ProjectKey, WaitingSince: r.StartedAt}
		if r.Hold != nil {
			v.ExpiresAt = r.Hold.ExpiresAt
			v.HoldCmd = r.Hold.Command
		}
		out = append(out, v)
	}
	return out, nil
}

// handleWSTicket issues the one-time ws ticket. A job credential is refused (an agent
// process has no business holding a browser push channel) and so is a worker token
// (a transport credential, not an identity).
func (s *Server) handleWSTicket(c *rux.Context) {
	switch callerKindFromCtx(c) {
	case callerKindJob:
		writeError(c, http.StatusForbidden, "ws not permitted for this caller", "job credentials cannot open the browser push channel")
		return
	case callerKindWorker:
		writeError(c, http.StatusForbidden, "ws not permitted for this caller", "worker tokens cannot request browser ws tickets")
		return
	}
	if s.wsTickets == nil {
		s.wsTickets = NewAttachTicketStore()
	}
	ticket := s.wsTickets.Issue(AttachTicketBinding{
		Caller: callerFromCtx(c),
		Mode:   "ws",
		Origin: c.Req.Header.Get("Origin"),
		Expiry: time.Now().Add(wsTicketTTL).Unix(),
		// Design §6「attach 与 ws 的 ticket 也带上调用方和依赖集合」: the WS handshake carries no
		// Authorization header, so the ticket binds the session the request named, the caller's
		// credential and the grants the decision rested on — what the channel's topics are
		// re-judged against (its permissions are the ones its SUBSCRIPTIONS need).
		Session: requestSessionID(c),
		Token:   presentedToken(c),
		Deps:    s.decisionOf(c).GrantIDs(),
		// 7798 第二期: the connection is judged by this caller's ceiling, resolved against the
		// live configuration when the channels subscribes and re-judged on it below.
		CeilingOwner: callerFromCtx(c),
	})
	c.JSON(http.StatusOK, map[string]any{"ticket": ticket, "expires_in": int(wsTicketTTL / time.Second)})
}

type wsClientFrame struct {
	T        string           `json:"t"`
	Topics   []string         `json:"topics,omitempty"`
	SinceSeq map[string]int64 `json:"since_seq,omitempty"`
}

// handleWS upgrades a ticketed request into a push connection. Like attach it answers
// a bad ticket / origin with a bare 401 (a WS handshake cannot carry the JSON envelope).
func (s *Server) handleWS(c *rux.Context) {
	if s.wsTickets == nil || s.live == nil {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}
	binding, ok := s.wsTickets.Consume(c.Req.URL.Query().Get("ticket"), time.Now().Unix())
	if !ok || binding.Mode != "ws" {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}
	if binding.Origin != "" && binding.Origin != c.Req.Header.Get("Origin") {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}
	// 7798 第二期: the ceiling of the caller the ticket was issued to governs the project range of
	// the topics (resolved now: a subscription arriving later re-asks the live configuration) and
	// the project restriction of the frames the hub hands this connection.
	ceil := s.wsCeiling(binding.CeilingOwner)
	pc, err := s.live.RegisterScoped(binding.Caller, ceilingConnScope(ceil))
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, pushhub.ErrTooManyConns) {
			code = http.StatusTooManyRequests
		}
		c.Resp.WriteHeader(code)
		return
	}
	defer pc.Close()

	var originPatterns []string
	if cfg := s.serverCfg(); cfg != nil {
		originPatterns = cfg.Governance.AttachOrigins
	}
	conn, err := websocket.Accept(c.Resp, c.Req, &websocket.AcceptOptions{
		OriginPatterns:  originPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(c.Req.Context())
	defer cancel()
	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	touch := func() { lastActive.Store(time.Now().UnixNano()) }

	// Design §6: the push channel is a LONG connection of the caller the ticket was issued to. It
	// starts resting on nothing (its permissions are the ones its subscriptions need, re-declared
	// on every sub / unsub below) and is closed when one of them goes away — the page reconnects
	// with its cursors and re-subscribes, and a topic it may still read comes back.
	spec := ticketLiveConnSpec(binding, liveConnWS, "push")
	// The push channel's range is pinned at connect time: a re-judgment that finds a different
	// ceiling (narrowed, widened or read-only) ends the connection, and the page reconnects with its
	// cursors and re-subscribes under the new range (gofer-7798 第二期, 计划 E3-1).
	spec.ceilingFP, spec.ceilingPinned = ceilingFingerprint(ceil), true
	spec.close = func(reason string) {
		slog.Info("authz.ws_dropped", "event", "authz.ws_dropped", "component", "server",
			"caller", binding.Caller, "session", binding.Session, "reason", reason)
		cancel()
	}
	releaseConn := s.registerLiveConn(spec)
	defer releaseConn.release()

	// Writer: the only goroutine that writes frames; it drains the connection's bounded
	// queue (and turns an overflow into a `resync`).
	go func() {
		defer cancel()
		for {
			b, err := pc.Next(ctx)
			if err != nil {
				if errors.Is(err, pushhub.ErrClosed) {
					_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
				}
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, wsWriteTimeout)
			err = conn.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return
			}
		}
	}()

	// Keepalive: a WS ping every 20s (the browser answers with a pong, which the reader
	// below observes) and a watchdog that drops a peer silent for 60s.
	go func() {
		t := time.NewTicker(wsPingEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, lastActive.Load())) > wsIdleTimeout {
					_ = conn.Close(wsCloseIdle, "idle timeout")
					cancel()
					return
				}
				pctx, pcancel := context.WithTimeout(ctx, wsPingEvery)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
				touch()
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		touch()
		var f wsClientFrame
		if json.Unmarshal(data, &f) != nil {
			pc.Push(pushhub.ErrorFrame("bad frame", nil))
			continue
		}
		switch f.T {
		case "sub":
			allowed, rejected := s.wsFilterTopics(c, ceil, binding, f.Topics)
			if rej := pc.Subscribe(allowed, f.SinceSeq); len(rej) > 0 {
				rejected = append(rejected, rej...)
			}
			s.wsRefreshTopicPerms(releaseConn, pc, c, binding)
			if len(rejected) > 0 {
				pc.Push(pushhub.ErrorFrame("unknown or excess topics", rejected))
			}
		case "unsub":
			pc.Unsubscribe(f.Topics)
			s.wsRefreshTopicPerms(releaseConn, pc, c, binding)
		case "ping":
			pc.Push(pushhub.PongFrame())
		default:
			pc.Push(pushhub.ErrorFrame("unknown frame type", nil))
		}
	}
}
