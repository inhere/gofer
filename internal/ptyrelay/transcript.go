package ptyrelay

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// transcriptFlushBytes / transcriptFlushInterval bound how much de-ANSI'd text a
// running pty session leaves unwritten: `job logs`/the web log page read the file
// while the job runs, so the tail must land within a couple of seconds (PTY-01).
const (
	transcriptFlushBytes    = 64 * 1024
	transcriptFlushInterval = 2 * time.Second
)

// ansiState is the byte-at-a-time state of Stripper's escape-sequence machine.
type ansiState uint8

const (
	stGround ansiState = iota
	stEsc
	stCSI
	stOSC
	stOSCEsc
	stIntermediate
)

// Stripper removes ANSI escape sequences (PTY-01 §四): CSI, OSC, the two-byte
// sequences and the charset/intermediate introducers, and folds CR into LF. It is
// a LEAF-level text filter with no screen model: the cursor motion a TUI uses to
// PLACE text is turned back into whitespace (see csiFinal) so the words it drew stay
// apart, and every other decoration is simply dropped.
//
// State is carried ACROSS Write calls: a read chunk may split an escape sequence
// (a 4KB pty read routinely cuts an OSC title or a colour run in half), and a
// stateless filter would leak the tail of the sequence into the text. A CR is
// also held back until the next byte, so CRLF folds to ONE newline and a final
// lone CR is emitted by Flush.
type Stripper struct {
	st   ansiState
	cr   bool
	csi  []byte // parameter/intermediate bytes of the CSI sequence in flight
	last byte   // last text byte emitted (0 = nothing emitted yet)
}

// csiParamBytesMax bounds the parameter bytes kept for the sequence in flight, and
// csiParamMax the CUF spacing a single sequence may produce: a TUI's padding is a
// handful of columns, while a corrupt or hostile stream must not be able to blow the
// transcript up.
const (
	csiParamBytesMax = 32
	csiParamMax      = 200
)

// emit appends one text byte and remembers it as the last byte out — what decides
// whether a following cursor move still has to separate words.
func (s *Stripper) emit(out []byte, b byte) []byte {
	s.last = b
	return append(out, b)
}

// Write returns p with escape sequences removed and CR folded to LF. Cursor motion
// that stood in for spacing comes back as whitespace (see csiFinal): a TUI draws text
// by MOVING the cursor rather than printing padding, so a plain drop ran words
// together.
func (s *Stripper) Write(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch s.st {
		case stGround:
			switch b {
			case 0x1b:
				s.st = stEsc
			case '\r':
				s.cr = true // held: CRLF must fold to a single newline
			case '\n':
				s.cr = false
				out = s.emit(out, '\n')
			default:
				if s.cr {
					s.cr = false
					out = s.emit(out, '\n')
				}
				out = s.emit(out, b)
			}
		case stEsc:
			switch b {
			case '[':
				s.st = stCSI
				s.csi = s.csi[:0] // a fresh sequence: the previous params are done with
			case ']':
				s.st = stOSC
			case '(', ')', '*', '+', '-', '.', '/', '#', '$', '%', '&', '"', ' ', '!':
				s.st = stIntermediate // one byte follows (charset / intermediate)
			default:
				s.st = stGround // two-byte sequence (ESC =, ESC >, ESC M, …)
			}
		case stCSI:
			switch {
			case b >= 0x40 && b <= 0x7e: // final byte: the sequence ends here
				out = s.csiFinal(out, b)
				s.st = stGround
			case b >= 0x20 && b <= 0x3f: // parameter / intermediate bytes
				if len(s.csi) < csiParamBytesMax {
					s.csi = append(s.csi, b)
				}
			}
			// Any other byte (a C0 control inside the sequence) stays swallowed: the
			// transcript must never leak a half-parsed escape.
		case stOSC:
			switch b {
			case 0x07: // BEL terminator
				s.st = stGround
			case 0x1b: // possible ST (ESC \)
				s.st = stOSCEsc
			}
		case stOSCEsc:
			if b == '\\' {
				s.st = stGround
			} else {
				s.st = stOSC // not ST: the sequence continues
			}
		case stIntermediate:
			s.st = stGround
		}
	}
	return out
}

// csiFinal handles the final byte of a CSI sequence. Layout-carrying sequences become
// whitespace instead of vanishing (F6): claude's ink TUI pads columns with `ESC[nC`
// (CUF, cursor forward), so a transcript that dropped it read
// `NewMCPserverfoundinthisproject…`. CUF emits n spaces (parameter default 1, clamped
// by csiParamMax); an absolute move (`ESC[nG` CHA, `ESC[r;cH` CUP) emits AT MOST one
// space — without a screen model the only thing worth keeping is that two words were
// drawn apart; erase (`ESC[K`/`ESC[J`) and every other sequence print nothing.
func (s *Stripper) csiFinal(out []byte, final byte) []byte {
	switch final {
	case 'C', 'G', 'H', 'f':
		if s.cr { // a held CR still owes its newline: it goes before any padding
			s.cr = false
			out = s.emit(out, '\n')
		}
	}
	switch final {
	case 'C':
		for n := s.csiParam(1); n > 0; n-- {
			out = s.emit(out, ' ')
		}
	case 'G', 'H', 'f':
		if s.last != 0 && !isTextSpace(s.last) {
			out = s.emit(out, ' ')
		}
	case 'K', 'J':
		// Erase in line / display: invisible, and it must not insert a blank either.
	}
	s.csi = s.csi[:0]
	return out
}

// csiParam returns the first parameter of the sequence in flight, or def when it is
// absent or unreadable — `ESC[C` means one column, and so does a private-mode form
// (`ESC[?25C`) because a `?` prefix is not a parameter this filter interprets. The
// value is clamped to csiParamMax.
func (s *Stripper) csiParam(def int) int {
	seg := s.csi
	for i := range s.csi {
		if s.csi[i] == ';' {
			seg = s.csi[:i]
			break
		}
	}
	n := 0
	for _, c := range seg {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
		if n > csiParamMax {
			return csiParamMax
		}
	}
	if n <= 0 {
		return def // `ESC[0C` moves one column, as ECMA-48 says
	}
	return n
}

// isTextSpace reports whether b is separation in the transcript's own terms — the
// bytes a TUI lets stand for a gap between words.
func isTextSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// Flush returns the newline a held lone CR owes and clears the pending state. It
// is what makes a transcript that ended with "\r" still close its last line.
func (s *Stripper) Flush() []byte {
	if !s.cr {
		return nil
	}
	s.cr = false
	return s.emit(nil, '\n')
}

// Transcript is the de-ANSI'd text record of one pty session, written to
// <result_dir>/pty.txt (PTY-01 §四): an interactive job's pty output never reaches
// stdout.log, so this file is the only durable text an operator (or
// `job logs`) can read after the TUI is gone.
//
// The retained text is bounded by maxBytes (pty.transcript_max_bytes, default
// 4MB): the ring keeps the TAIL and the file is compacted down to it, so a
// long-running TUI transcript stays readable rather than growing without bound.
// Writes append only what is new (recording must stay cheap); a compaction — the
// one operation that rewrites the file — happens at most once per maxBytes/4
// written bytes, so the file is bounded at maxBytes*1.25 while over the cap.
//
// It is safe for concurrent use, but the relay only ever writes it from the
// recorder goroutine and closes it from finish().
type Transcript struct {
	path string
	max  int

	mu           sync.Mutex
	strip        Stripper
	ring         *ring
	pending      []byte
	file         *os.File
	fileSize     int64
	sinceCompact int64
	lastFlush    time.Time
	closed       bool
	err          error
}

// NewTranscript opens no file: the sink is created lazily on the first flush, so
// a pty session that never produces output leaves no empty pty.txt behind.
// maxBytes <= 0 falls back to DefaultTranscriptMaxBytes.
func NewTranscript(path string, maxBytes int) *Transcript {
	if maxBytes <= 0 {
		maxBytes = DefaultTranscriptMaxBytes
	}
	return &Transcript{path: path, max: maxBytes, ring: newRing(maxBytes), lastFlush: time.Now()}
}

// DefaultTranscriptMaxBytes is the transcript tail cap when
// pty.transcript_max_bytes is unset (PTY-01 decision 3).
const DefaultTranscriptMaxBytes = 4 << 20

// Path is the file the transcript is written to.
func (t *Transcript) Path() string { return t.path }

// Write strips p and records the result: the ring keeps the tail (the bounded
// transcript), the pending buffer feeds the next flush. The returned n is the
// number of RAW bytes consumed — the relay's recorder treats this like the cast
// sink, so a short write would be lost output.
func (t *Transcript) Write(p []byte) (int, error) {
	text := t.strip.Write(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || len(text) == 0 {
		return len(p), t.err
	}
	t.ring.Write(text)
	t.pending = append(t.pending, text...)
	if len(t.pending) >= transcriptFlushBytes || time.Since(t.lastFlush) >= transcriptFlushInterval {
		t.flushLocked(false)
	}
	return len(p), t.err
}

// Close flushes the tail and seals the file. Idempotent: the relay's finish and
// an explicit teardown may both call it.
func (t *Transcript) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return t.err
	}
	t.closed = true
	if tail := t.strip.Flush(); len(tail) > 0 {
		t.ring.Write(tail)
		t.pending = append(t.pending, tail...)
	}
	t.flushLocked(true)
	if t.file != nil {
		if cerr := t.file.Close(); cerr != nil && t.err == nil {
			t.err = cerr
		}
		t.file = nil
	}
	return t.err
}

// Tail returns the retained (de-ANSI'd) transcript tail — what the file holds
// after the cap, and what the session-id capture re-scans at close.
func (t *Transcript) Tail() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ring.Snapshot()
}

// Err reports the first write/close error, if any.
func (t *Transcript) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// flushLocked appends the pending text to the file and compacts it back to the
// tail when it has outgrown the cap. force also compacts below the
// once-per-quarter-max throttle (used by Close, where the final size is what
// matters).
func (t *Transcript) flushLocked(force bool) {
	if len(t.pending) == 0 {
		return
	}
	if t.file == nil {
		f, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			if t.err == nil {
				t.err = fmt.Errorf("pty transcript %s: %w", t.path, err)
			}
			t.pending = nil
			return
		}
		t.file = f
	}
	n, err := t.file.Write(t.pending)
	t.fileSize += int64(n)
	t.sinceCompact += int64(n)
	if err != nil {
		if t.err == nil {
			t.err = fmt.Errorf("pty transcript %s: %w", t.path, err)
		}
	}
	t.pending = nil
	t.lastFlush = time.Now()
	if t.err == nil && t.fileSize > int64(t.max) && (force || t.sinceCompact >= int64(t.max/4)) {
		t.compactLocked()
	}
}

// compactLocked rewrites the file as the ring's tail (the retained transcript),
// which is how the 4MB cap is enforced on disk rather than only in memory.
func (t *Transcript) compactLocked() {
	tail := t.ring.Snapshot()
	if err := t.file.Truncate(0); err != nil {
		if t.err == nil {
			t.err = fmt.Errorf("pty transcript %s: %w", t.path, err)
		}
		return
	}
	if _, err := t.file.Seek(0, 0); err != nil {
		if t.err == nil {
			t.err = fmt.Errorf("pty transcript %s: %w", t.path, err)
		}
		return
	}
	n, err := t.file.Write(tail)
	if err != nil && t.err == nil {
		t.err = fmt.Errorf("pty transcript %s: %w", t.path, err)
	}
	t.fileSize = int64(n)
	t.sinceCompact = 0
}
