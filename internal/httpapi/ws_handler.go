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

// pendingSnapshot is the bell's data: what GET /v1/interactions and
// GET /v1/decisions?state=OPEN return, in one payload.
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
	if inter == nil {
		inter = []job.Interaction{}
	}
	return map[string]any{"interactions": inter, "decisions": views}, nil
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
	pc, err := s.live.Register(binding.Caller)
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
			allowed, rejected := s.wsFilterTopics(c, binding, f.Topics)
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
