package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/ptyrelay"
)

const (
	attachCloseInvalidTicket = websocket.StatusCode(4401)
	attachCloseNotFound      = websocket.StatusCode(4404)
	attachResizeMinCols      = 1
	attachResizeMaxCols      = 500
	attachResizeMinRows      = 1
	attachResizeMaxRows      = 200
)

type attachClientFrame struct {
	Type string `json:"t"`
	Data string `json:"d,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

func (s *Server) handleJobAttach(c *rux.Context) {
	ticket := c.Req.URL.Query().Get("ticket")
	if s.attachTickets == nil {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}
	binding, ok := s.attachTickets.Consume(ticket, time.Now().Unix())
	if !ok {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}
	jobID := c.Param("id")
	if binding.JobID != jobID {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}
	if binding.Origin != "" && binding.Origin != c.Req.Header.Get("Origin") {
		c.Resp.WriteHeader(http.StatusUnauthorized)
		return
	}

	var originPatterns []string
	if s.cfg != nil {
		originPatterns = s.cfg.Governance.AttachOrigins
	}
	conn, err := websocket.Accept(c.Resp, c.Req, &websocket.AcceptOptions{
		OriginPatterns:  originPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "attach closed")

	ctx := c.Req.Context()
	closeWS := func(code websocket.StatusCode, reason string) {
		_ = conn.Close(code, reason)
	}
	if s.ptyRelays == nil {
		closeWS(attachCloseNotFound, "relay not found")
		return
	}
	entry, ok := s.ptyRelays.Lookup(binding.JobID)
	if !ok || entry.Relay == nil || (entry.State != ptyrelay.RelayOpen && entry.State != ptyrelay.RelayAttached) {
		closeWS(attachCloseNotFound, "relay not open")
		return
	}
	if binding.PtySessionID == "" || binding.PtySessionID != entry.Binding.PtySessionID {
		closeWS(attachCloseNotFound, "relay binding mismatch")
		return
	}
	entry, err = s.ptyRelays.MarkAttached(binding.JobID)
	if err != nil || entry.Relay == nil {
		closeWS(attachCloseNotFound, "relay not open")
		return
	}
	relay := entry.Relay

	wantLease := binding.Mode != "read"
	viewer, err := relay.AddViewer(wantLease)
	gotLease := wantLease && err == nil
	if errors.Is(err, ptyrelay.ErrLeaseTaken) {
		viewer, err = relay.AddViewer(false)
		gotLease = false
	}
	if err != nil {
		closeWS(attachCloseNotFound, "viewer refused")
		return
	}
	defer viewer.Close()

	var writeMu sync.Mutex
	writeBinary := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(ctx, websocket.MessageBinary, b)
	}
	writeControl := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(ctx, websocket.MessageText, b)
	}

	// hello carries the relay's CURRENT size, not the binding's initial one: the
	// pty may have been resized since submit, and a client that starts from a
	// stale size renders garbage until the next resize (tools-3xy R1). Fall back
	// to the binding only when the relay never learned a size.
	helloCols, helloRows := relay.Size()
	if helloCols <= 0 || helloRows <= 0 {
		helloCols, helloRows = entry.Binding.Cols, entry.Binding.Rows
	}
	if err := writeControl(map[string]any{
		"t":     "hello",
		"write": gotLease,
		"cols":  helloCols,
		"rows":  helloRows,
	}); err != nil {
		return
	}

	// Follow pty size changes made by whoever holds the write lease: without this
	// broadcast every other viewer keeps a layout the TUI no longer draws for and
	// has no way to recover (tools-3xy R2). Registered after hello so the first
	// size a client sees is the hello's.
	viewer.SetSizeListener(func(cols, rows int) {
		_ = writeControl(map[string]any{"t": "r", "cols": cols, "rows": rows})
	})

	if scroll := relay.Scrollback(); len(scroll) > 0 {
		if err := writeBinary(scroll); err != nil {
			return
		}
	}

	pumpDone := make(chan bool, 1)
	go func() {
		endedByRelay := true
		defer func() {
			pumpDone <- endedByRelay
			close(pumpDone)
		}()
		for chunk := range viewer.Out() {
			if err := writeBinary(chunk); err != nil {
				endedByRelay = false
				return
			}
		}
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		s.readAttachFrames(ctx, conn, viewer, relay)
	}()

	writeExitAndClose := func() {
		// relay 关闭与 job 终态跨连接竞态；终态已可见才附 exit code。
		if s.jobs != nil {
			if res, ok := s.jobs.Get(binding.JobID); ok && job.IsTerminal(res.Status) {
				_ = writeControl(map[string]any{"t": "x", "code": res.ExitCode})
			} else {
				_ = writeControl(map[string]any{"t": "x"})
			}
		} else {
			_ = writeControl(map[string]any{"t": "x"})
		}
		closeWS(websocket.StatusNormalClosure, "session ended")
	}

	select {
	case <-relay.Done():
		writeExitAndClose()
	case endedByRelay := <-pumpDone:
		if endedByRelay {
			<-relay.Done()
			writeExitAndClose()
		}
	case <-readDone:
	case <-ctx.Done():
	}
}

func (s *Server) readAttachFrames(ctx context.Context, conn *websocket.Conn, viewer *ptyrelay.Viewer, relay *ptyrelay.Relay) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var frame attachClientFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			_ = conn.Close(websocket.StatusProtocolError, "bad attach frame")
			return
		}
		switch frame.Type {
		case "i":
			input, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				_ = conn.Close(websocket.StatusProtocolError, "bad input frame")
				return
			}
			_ = viewer.SendInput(input)
		case "r":
			// Only the write-lease holder may resize the shared pty (tools-3xy R3).
			// The front end already gates this on writeGranted; enforcing it here too
			// stops a read-only client (old build, crafted frame) from squeezing the
			// pty to its own viewport and garbling every other viewer. Ignored, not a
			// protocol error, to tolerate older front ends.
			if viewer.HoldsLease() &&
				frame.Cols >= attachResizeMinCols && frame.Cols <= attachResizeMaxCols &&
				frame.Rows >= attachResizeMinRows && frame.Rows <= attachResizeMaxRows {
				_ = relay.Resize(frame.Cols, frame.Rows)
			}
		default:
			_ = conn.Close(websocket.StatusProtocolError, "unknown attach frame")
			return
		}
	}
}
