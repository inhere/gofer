// Package castrec records a pty relay's output stream as an asciinema v2 "cast"
// file, optionally wrapped in a framed AES-256-GCM envelope (D-P3-4). It is a
// crypto-security-critical, stdlib-only package (G031): no third-party module is
// imported. httpapi constructs a Recorder at serve start and, per pty session,
// Opens a CastSink that ptyrelay writes output bytes to; on Close the sink flushes
// (plaintext) or seals its final authenticated frame (encrypted).
package castrec

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// asciinema v2 defaults when the pty binding reports no size (D-P3-2 fallback,
// aligned with the pty runner's 80x24 default).
const (
	defaultCols = 80
	defaultRows = 24
)

// asciicastHeader is the first line of an asciinema v2 file. Timestamp is the
// session start in Unix seconds; Width/Height are the initial terminal size.
type asciicastHeader struct {
	Version   int   `json:"version"`
	Width     int   `json:"width"`
	Height    int   `json:"height"`
	Timestamp int64 `json:"timestamp"`
}

// asciicastWriter turns pty output blocks into asciinema v2 event lines. It wraps
// an io.WriteCloser which is either the raw *os.File (plaintext) or an *EncWriter
// (encrypted); Close finalizes that chain. Write and Close are never called
// concurrently — ptyrelay's recordLoop is the sole owner (T1), so no lock is held.
//
// The clock is injected (now func) so tests can pin elapsed times; production
// passes time.Now.
type asciicastWriter struct {
	wc     io.WriteCloser
	now    func() time.Time
	start  time.Time
	err    error
	closed bool
}

// newAsciicastWriter writes the header line immediately (so even an empty session
// yields a valid file) and returns a sink ready for Write. cols/rows <= 0 fall
// back to 80x24. now defaults to time.Now when nil.
func newAsciicastWriter(wc io.WriteCloser, cols, rows int, startedAt int64, now func() time.Time) (*asciicastWriter, error) {
	if now == nil {
		now = time.Now
	}
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}
	a := &asciicastWriter{wc: wc, now: now}
	a.start = now()
	hdr, err := json.Marshal(asciicastHeader{Version: 2, Width: cols, Height: rows, Timestamp: startedAt})
	if err != nil {
		a.err = err
		return nil, fmt.Errorf("castrec: marshal header: %w", err)
	}
	hdr = append(hdr, '\n')
	if _, err := a.wc.Write(hdr); err != nil {
		a.err = err
		return nil, fmt.Errorf("castrec: write header: %w", err)
	}
	return a, nil
}

// Write records one output block as an asciinema event line
// `[elapsed, "o", "<data>"]` (raw bytes JSON-escaped). It reports len(p) written
// on success so the recordLoop sees the whole block consumed; a write error is
// latched into Err() and returned. Empty blocks are ignored.
func (a *asciicastWriter) Write(p []byte) (int, error) {
	if a.err != nil {
		return 0, a.err
	}
	if a.closed {
		return 0, errWriteAfterClose
	}
	if len(p) == 0 {
		return 0, nil
	}
	elapsed := a.now().Sub(a.start).Seconds()
	line, err := json.Marshal([]any{elapsed, "o", string(p)})
	if err != nil {
		a.err = err
		return 0, fmt.Errorf("castrec: marshal event: %w", err)
	}
	line = append(line, '\n')
	if _, err := a.wc.Write(line); err != nil {
		a.err = err
		return 0, err
	}
	return len(p), nil
}

// Close finalizes the underlying chain (flush + close the file for plaintext, or
// write the authenticated final frame + close for encrypted). It is idempotent
// and returns the latched error (if any). A close error is folded into Err().
func (a *asciicastWriter) Close() error {
	if a.closed {
		return a.err
	}
	a.closed = true
	if cerr := a.wc.Close(); cerr != nil && a.err == nil {
		a.err = cerr
	}
	return a.err
}

// Err reports the latched write/close error (including an encrypted sink's
// bounded-close failure). httpapi consults it at finalize to decide whether the
// recording_uri is durable (D-P3-1 / H5).
func (a *asciicastWriter) Err() error { return a.err }
