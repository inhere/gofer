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

	"github.com/inhere/gofer/internal/ptyrelay"
)

const (
	attachCloseInvalidTicket = websocket.StatusCode(4401)
	attachCloseNotFound      = websocket.StatusCode(4404)
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
	entry, err = s.ptyRelays.MarkAttached(binding.JobID)
	if err != nil || entry.Relay == nil {
		closeWS(attachCloseNotFound, "relay not open")
		return
	}
	relay := entry.Relay

	wantLease := binding.Mode != "read"
	viewer, err := relay.AddViewer(wantLease)
	if errors.Is(err, ptyrelay.ErrLeaseTaken) {
		viewer, err = relay.AddViewer(false)
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

	if scroll := relay.Scrollback(); len(scroll) > 0 {
		if err := writeBinary(scroll); err != nil {
			return
		}
	}

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for chunk := range viewer.Out() {
			if err := writeBinary(chunk); err != nil {
				return
			}
		}
	}()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		s.readAttachFrames(ctx, conn, viewer, relay)
	}()

	select {
	case <-relay.Done():
		closeWS(attachCloseNotFound, "relay closed")
	case <-pumpDone:
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
			if frame.Cols > 0 && frame.Rows > 0 {
				_ = relay.Resize(frame.Cols, frame.Rows)
			}
		default:
			_ = conn.Close(websocket.StatusProtocolError, "unknown attach frame")
			return
		}
	}
}
