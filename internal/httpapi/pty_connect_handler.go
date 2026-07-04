package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
)

const (
	ptyCloseInvalidNonce = websocket.StatusCode(4401)
	ptyCloseNotFound     = websocket.StatusCode(4404)
	ptyCloseInstanceGone = websocket.StatusCode(4409)
)

type ptyConnectHello struct {
	JobID        string `json:"job_id"`
	PtySessionID string `json:"pty_session_id"`
	RelayNonce   string `json:"relay_nonce"`
}

func (s *Server) handlePtyConnect(c *rux.Context) {
	got, ok := bearerToken(c.Req.Header.Get("Authorization"))
	callerID, matched := "", false
	if ok {
		callerID, matched = s.lookupCaller(got)
	}
	if !matched {
		slog.Warn("worker auth rejected at pty upgrade", "remote", c.Req.RemoteAddr,
			"reason", "missing or unknown bearer token")
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(c.Resp, c.Req, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 * 1024)

	ctx := c.Req.Context()
	closeWS := func(code websocket.StatusCode, reason string) {
		_ = conn.Close(code, reason)
	}

	var hello ptyConnectHello
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		closeWS(websocket.StatusProtocolError, "bad hello")
		return
	}

	binding, ok := s.relayNonces.Consume(hello.RelayNonce, time.Now().Unix())
	if !ok {
		closeWS(ptyCloseInvalidNonce, "invalid relay nonce")
		return
	}
	instanceID, live := s.hub.LiveInstance(binding.WorkerID)
	if !live || instanceID != binding.InstanceID || callerID != binding.WorkerID {
		closeWS(ptyCloseInstanceGone, "worker instance mismatch")
		return
	}
	if binding.JobID != hello.JobID || binding.PtySessionID != hello.PtySessionID {
		closeWS(ptyCloseNotFound, "relay binding mismatch")
		return
	}
	if s.jobs == nil {
		s.ptyRelays.Close(binding.JobID, "job_not_live")
		closeWS(ptyCloseNotFound, "job not live")
		return
	}
	res, ok := s.jobs.Get(binding.JobID)
	if !ok || !res.Interactive || job.IsTerminal(res.Status) {
		s.ptyRelays.Close(binding.JobID, "job_not_live")
		closeWS(ptyCloseNotFound, "job not live")
		return
	}

	source := newRemotePtySource(ctx, conn)
	entry, err := s.ptyRelays.Open(hello.RelayNonce, source)
	if err != nil {
		closeWS(ptyCloseNotFound, "relay not pending")
		return
	}
	defer s.ptyRelays.Close(binding.JobID, "pty_ws_closed")

	select {
	case <-entry.Relay.Done():
	case <-ctx.Done():
	}
}
