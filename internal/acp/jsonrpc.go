package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// JSON-RPC 2.0 error codes we emit or care about.
const (
	CodeParseError     = -32700
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeMethodNotFound = -32601
)

// Message is one JSON-RPC 2.0 message. ACP puts exactly one message per stdio
// line, so the codec below is line-oriented.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// isRequest reports an agent→client request (method + id).
func (m *Message) isRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// isResponse reports a reply to one of our requests (id, no method).
func (m *Message) isResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error implements error.
func (e *RPCError) Error() string {
	return fmt.Sprintf("acp: jsonrpc error %d: %s", e.Code, e.Message)
}

// codec writes newline-delimited JSON messages. Writes are serialised because
// requests, notifications and responses are produced by different goroutines
// (the caller's goroutine, its cancel path, and the read loop).
type codec struct {
	mu sync.Mutex
	w  io.Writer
}

func (c *codec) write(msg *Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("acp: encode message: %w", err)
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.w.Write(b); err != nil {
		return fmt.Errorf("acp: write message: %w", err)
	}
	return nil
}

// reader reads newline-delimited JSON messages.
type reader struct {
	r *bufio.Reader
}

// read returns the next message, skipping blank lines. A line that is not valid
// JSON is a protocol violation and is reported as an error (the caller fails the
// client): silently dropping it would desynchronise request/response pairing.
func (r *reader) read() (*Message, error) {
	for {
		line, err := r.r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			var msg Message
			if jerr := json.Unmarshal(trimmed, &msg); jerr != nil {
				return nil, fmt.Errorf("acp: invalid message %q: %w", trimmed, jerr)
			}
			return &msg, nil
		}
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("acp: read message: %w", err)
		}
	}
}
