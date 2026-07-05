package castrec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// CastSink is the cast recording sink returned by Recorder.Open. ptyrelay writes
// pty output via io.Writer and finalizes via Close; httpapi additionally consults
// Err() at finalize (H5). It is defined here rather than imported from ptyrelay so
// castrec stays leaf on the crypto side — the concrete asciicastWriter also
// satisfies ptyrelay's narrower {io.Writer; Close} interface (wired in T1/T5).
type CastSink interface {
	io.Writer
	Close() error
	Err() error
}

// Recorder is the per-serve cast recording factory. It holds the (once-derived)
// master key when encryption is on and mints a CastSink per pty session. A nil
// *Recorder is never used — httpapi injects nil to mean "recording disabled".
type Recorder struct {
	enabled   bool
	encrypt   bool
	masterKey []byte
	now       func() time.Time // injectable clock for the asciicast timeline (tests)
}

// New builds a Recorder from the cast config and the resolved (decoded) secret.
// When encryption is on it derives the master key once (HKDF); a derivation error
// is propagated so serve start can fail fast. secret is only consulted when both
// cast and encryption are enabled; its length is validated at serve start (T4).
//
// Deviation from the plan snippet `New(...) *Recorder`: New returns an error
// because hkdf.Key (Go1.24+) returns (key, error) — the master-key derivation
// must surface its failure rather than be swallowed.
func New(cfg config.CastConfig, secret []byte) (*Recorder, error) {
	r := &Recorder{
		enabled: cfg.Enabled,
		encrypt: cfg.Enabled && cfg.Encryption.Enabled,
		now:     time.Now,
	}
	if r.encrypt {
		mk, err := deriveMaster(secret)
		if err != nil {
			return nil, fmt.Errorf("castrec: derive master key: %w", err)
		}
		r.masterKey = mk
	}
	return r, nil
}

// Encrypted reports whether Open produces encrypted (framed AEAD) recordings.
func (r *Recorder) Encrypted() bool { return r.encrypt }

// Open creates the cast file at path and returns a CastSink. When encryption is
// on the asciicast stream is wrapped in an EncWriter; otherwise it is written as
// plaintext asciinema v2. cols/rows <= 0 fall back to 80x24 in the asciicast
// header. A file-create (or header-write) failure returns an error so the caller
// can degrade to "not recording".
func (r *Recorder) Open(path string, cols, rows int, startedAt int64) (CastSink, error) {
	if !r.enabled {
		return nil, errors.New("castrec: recorder disabled")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("castrec: create %q: %w", path, err)
	}
	var wc io.WriteCloser = f
	if r.encrypt {
		ew, err := newEncWriter(f, r.masterKey)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		wc = ew
	}
	sink, err := newAsciicastWriter(wc, cols, rows, startedAt, r.now)
	if err != nil {
		_ = wc.Close()
		return nil, err
	}
	return sink, nil
}

// NewDecReader opens an encrypted cast file for streaming decryption (the
// recording download gate, T6). The returned ReadCloser yields the decrypted
// asciicast bytes and closes the file on Close. It is only valid for encrypted
// recorders; a plaintext file is served directly (http.ServeFile) instead.
func (r *Recorder) NewDecReader(path string) (io.ReadCloser, error) {
	if !r.encrypt {
		return nil, errors.New("castrec: NewDecReader on a non-encrypting recorder")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("castrec: open %q: %w", path, err)
	}
	dr, err := newDecReader(f, r.masterKey)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	dr.c = f
	return dr, nil
}
