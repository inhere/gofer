package httpapi

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
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

	// Cast recording (WEB-03 P3, D-P3-1/3). Recording is opt-in: with no recorder
	// wired (mcp/tests, recording disabled) sink stays nil and behaviour is
	// unchanged (G023). A per-session pty.cast sink is minted under the job's
	// result dir; an Open failure degrades to "not recording" (empty recURI) and
	// never blocks the relay. The pty_sessions row is recorded independent of the
	// sink — even with recording off (empty recURI) the session is tracked.
	startedAt := time.Now().Unix()
	var sink castrec.CastSink
	recURI := ""
	encrypted := false
	// Cols/Rows for the cast header + session row come from the prepared relay
	// binding (set by the worker at Prepare, D-P3-2); the nonce binding carries no
	// window size. 0 falls back to 80x24 in the cast header.
	var cols, rows int
	if pending, ok := s.ptyRelays.Lookup(binding.JobID); ok {
		cols, rows = pending.Binding.Cols, pending.Binding.Rows
	}
	if s.castRecorder != nil {
		uri := filepath.Join(res.ResultDir, "pty.cast")
		if cs, cerr := s.castRecorder.Open(uri, cols, rows, startedAt); cerr == nil {
			sink = cs
			recURI = uri
			encrypted = s.castRecorder.Encrypted()
		} else {
			slog.Warn("pty cast recording open failed", "job", binding.JobID, "err", cerr)
		}
	}

	source := newRemotePtySource(ctx, conn)
	var opts []ptyrelay.Option
	if sink != nil {
		opts = append(opts, ptyrelay.WithCast(sink))
	}
	if obs := s.sessionIDObserver(res); obs != nil {
		opts = append(opts, ptyrelay.WithOutputObserver(obs))
	}
	entry, err := s.ptyRelays.Open(hello.RelayNonce, source, opts...)
	if err != nil {
		// The relay never opened, so no recordLoop will run finish to close the
		// sink: close the just-minted sink here so the file is not left dangling.
		// Record NO session row — the pty was never established (H4).
		if sink != nil {
			_ = sink.Close()
		}
		closeWS(ptyCloseNotFound, "relay not pending")
		return
	}

	// pty_sessions open row (single writer = httpapi, D-P3-3). nil-safe: no store
	// wired (mcp/tests) → no row.
	s.upsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: binding.PtySessionID,
		JobID:        binding.JobID,
		WorkerID:     binding.WorkerID,
		InstanceID:   binding.InstanceID,
		Owner:        res.CallerID,
		State:        "open",
		Cols:         cols,
		Rows:         rows,
		RecordingURI: recURI,
		Encrypted:    encryptedFlag(encrypted),
		StartedAt:    startedAt,
	})

	// Single finalize point (D-P3-1): close the relay, wait for the recorder to
	// seal the cast tail (Done = finish, bounded by castCloseGrace — never waits
	// forever, even if the sink's Close wedges), then write the closed snapshot. A
	// cast write/close failure blanks recording_uri (H5) so the download gate never
	// offers a broken file.
	defer func() {
		s.ptyRelays.Close(binding.JobID, "pty_ws_closed")
		<-entry.Relay.Done()
		uriFinal := recURI
		// A cast write failure (sink.Err) OR a wedged cast Close that timed out in
		// boundedCastClose (CastClosedCleanly()==false, H1) means the recording was
		// not sealed — blank recording_uri so the download gate never offers a
		// half-written / un封尾ed file.
		if (sink != nil && sink.Err() != nil) || !entry.Relay.CastClosedCleanly() {
			uriFinal = ""
		}
		s.upsertPtySession(jobstore.PtySessionRecord{
			PtySessionID: binding.PtySessionID,
			JobID:        binding.JobID,
			WorkerID:     binding.WorkerID,
			InstanceID:   binding.InstanceID,
			Owner:        res.CallerID,
			State:        "closed",
			Cols:         cols,
			Rows:         rows,
			RecordingURI: uriFinal,
			Encrypted:    encryptedFlag(encrypted),
			BytesIn:      entry.Relay.InputLen(),
			BytesOut:     int64(entry.Relay.RecordedLen()),
			StartedAt:    startedAt,
			EndedAt:      time.Now().Unix(),
		})
	}()

	select {
	case <-entry.Relay.Done():
	case <-ctx.Done():
	}
}

// encryptedFlag maps the recording-encrypted bool to the SR301 int convention
// (1 = yes, 2 = no; never 0).
func encryptedFlag(enc bool) int {
	if enc {
		return 1
	}
	return 2
}

// upsertPtySession persists a pty_sessions row when the store is wired; a nil
// store (mcp / most tests) is a no-op. Errors are logged and swallowed — pty
// session persistence is audit metadata and must never break the live relay.
func (s *Server) upsertPtySession(rec jobstore.PtySessionRecord) {
	if s.ptySessions == nil {
		return
	}
	if err := s.ptySessions.UpsertPtySession(rec); err != nil {
		slog.Warn("pty session upsert failed", "job", rec.JobID, "state", rec.State, "err", err)
	}
}
