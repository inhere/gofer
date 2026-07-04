package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/coder/websocket"
)

type remotePtySource struct {
	ctx   context.Context
	conn  *websocket.Conn
	wmu   sync.Mutex
	cmu   sync.Mutex
	once  sync.Once
	left  []byte
	close error
}

func newRemotePtySource(ctx context.Context, conn *websocket.Conn) *remotePtySource {
	return &remotePtySource{ctx: ctx, conn: conn}
}

func (s *remotePtySource) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		s.cmu.Lock()
		if len(s.left) > 0 {
			n := copy(p, s.left)
			s.left = s.left[n:]
			s.cmu.Unlock()
			return n, nil
		}
		s.cmu.Unlock()

		typ, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return 0, err
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		n := copy(p, data)
		if n < len(data) {
			s.cmu.Lock()
			s.left = append(s.left[:0], data[n:]...)
			s.cmu.Unlock()
		}
		return n, nil
	}
}

func (s *remotePtySource) Write(p []byte) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := s.conn.Write(s.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *remotePtySource) Resize(cols, rows int) error {
	msg, err := json.Marshal(struct {
		Type string `json:"type"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}{Type: "resize", Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.Write(s.ctx, websocket.MessageText, msg)
}

func (s *remotePtySource) Close() error {
	s.once.Do(func() {
		s.close = s.conn.Close(websocket.StatusNormalClosure, "pty source closed")
	})
	if s.close == nil {
		return nil
	}
	if websocket.CloseStatus(s.close) == websocket.StatusNormalClosure {
		return nil
	}
	if s.close == io.ErrClosedPipe {
		return nil
	}
	return s.close
}
