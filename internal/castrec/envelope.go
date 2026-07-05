package castrec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// framed AEAD constants (D-P3-4). The on-disk file is:
//
//	header = magic(4) || version(1) || fileID(16)
//	frame  = uint32BE(ct_len) || ciphertext(=plaintext||GCM tag)
//
// Each frame's AAD binds {magic, version, fileID, frame_index, is_final,
// plaintext_len}; the nonce is a per-file counter. Because the file key is
// derived per file (fileID as HKDF salt), a counter-only nonce never repeats
// across files. The final frame (is_final=1, empty plaintext) is authenticated
// and MUST be the physical end of the file — any missing final or trailing bytes
// after it is corruption.
const (
	castMagic       = "GFC1" // 4-byte file magic
	castFormatVer   = 1      // 1-byte format version (key rotation / algo upgrade room)
	castFileIDLen   = 16     // random per-file id (per-file key salt + AAD binding)
	castChunkSize   = 16 * 1024
	castNonceLen    = 12
	castHeaderLen   = 4 + 1 + castFileIDLen
	castHeaderInfo  = "gofer-pty-cast/v1"
	castFileKeyInfo = "gofer-pty-cast-file/v1"
)

var (
	errBadMagic           = errors.New("castrec: bad file magic")
	errUnsupportedVersion = errors.New("castrec: unsupported format version")
	errMissingFinal       = errors.New("castrec: truncated (EOF before authenticated final frame)")
	errTrailingAfterFinal = errors.New("castrec: trailing bytes after final frame")
	errFrameTooLarge      = errors.New("castrec: frame length exceeds bound")
	errFrameCorrupt       = errors.New("castrec: frame corrupt (short ciphertext)")
	errWriteAfterClose    = errors.New("castrec: write after close")
)

// deriveMaster derives the master key from the resolved secret (serve start,
// once). hkdf.Key (Go1.24+) returns (key, error) — the error is propagated.
func deriveMaster(secret []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, secret, nil, castHeaderInfo, 32)
}

// deriveFileKey derives a per-file key from the master key using fileID as salt,
// so each file has an independent key (removes any cross-file nonce reuse).
func deriveFileKey(mk, fileID []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, mk, fileID, castFileKeyInfo, 32)
}

// castNonce builds the 12-byte counter nonce: uint32BE(0) || uint64BE(frameIdx).
func castNonce(frameIdx uint64) []byte {
	var n [castNonceLen]byte
	binary.BigEndian.PutUint64(n[4:], frameIdx) // first 4 bytes stay zero
	return n[:]
}

// castAAD builds the additional authenticated data for one frame.
func castAAD(fileID []byte, frameIdx uint64, isFinal bool, ptLen uint32) []byte {
	aad := make([]byte, 0, 4+1+len(fileID)+8+1+4)
	aad = append(aad, castMagic...)
	aad = append(aad, byte(castFormatVer))
	aad = append(aad, fileID...)
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], frameIdx)
	aad = append(aad, idx[:]...)
	if isFinal {
		aad = append(aad, 1)
	} else {
		aad = append(aad, 0)
	}
	var pl [4]byte
	binary.BigEndian.PutUint32(pl[:], ptLen)
	aad = append(aad, pl[:]...)
	return aad
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("castrec: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("castrec: gcm: %w", err)
	}
	if gcm.NonceSize() != castNonceLen {
		return nil, fmt.Errorf("castrec: unexpected gcm nonce size %d", gcm.NonceSize())
	}
	return gcm, nil
}

// EncWriter is the framed AEAD writer. Write accumulates plaintext into <=16KB
// frames (sealed and written as they fill); Close flushes any pending partial
// frame, then writes the authenticated final frame and closes the underlying
// file. Err() exposes the latched write error.
type EncWriter struct {
	wc       io.WriteCloser
	gcm      cipher.AEAD
	fileID   []byte
	buf      []byte
	frameIdx uint64
	err      error
	closed   bool
}

// newEncWriter generates a random fileID, derives the per-file key, writes the
// file header and returns a writer ready for Write. The caller retains ownership
// of wc on error (it is not closed here).
func newEncWriter(wc io.WriteCloser, masterKey []byte) (*EncWriter, error) {
	fileID := make([]byte, castFileIDLen)
	if _, err := rand.Read(fileID); err != nil {
		return nil, fmt.Errorf("castrec: gen fileID: %w", err)
	}
	fk, err := deriveFileKey(masterKey, fileID)
	if err != nil {
		return nil, fmt.Errorf("castrec: derive file key: %w", err)
	}
	gcm, err := newGCM(fk)
	if err != nil {
		return nil, err
	}
	e := &EncWriter{wc: wc, gcm: gcm, fileID: fileID, buf: make([]byte, 0, castChunkSize)}
	hdr := make([]byte, 0, castHeaderLen)
	hdr = append(hdr, castMagic...)
	hdr = append(hdr, byte(castFormatVer))
	hdr = append(hdr, fileID...)
	if _, err := wc.Write(hdr); err != nil {
		e.err = err
		return nil, fmt.Errorf("castrec: write header: %w", err)
	}
	return e, nil
}

// Write accumulates p into frames, sealing and writing each frame as it reaches
// castChunkSize. A partial frame stays buffered until Close.
func (e *EncWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	if e.closed {
		return 0, errWriteAfterClose
	}
	written := 0
	for len(p) > 0 {
		space := castChunkSize - len(e.buf)
		n := min(len(p), space)
		e.buf = append(e.buf, p[:n]...)
		p = p[n:]
		written += n
		if len(e.buf) == castChunkSize {
			if err := e.flushFrame(false); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

// flushFrame seals e.buf as one frame (data or final) and resets the buffer.
func (e *EncWriter) flushFrame(isFinal bool) error {
	ptLen := uint32(len(e.buf))
	nonce := castNonce(e.frameIdx)
	aad := castAAD(e.fileID, e.frameIdx, isFinal, ptLen)
	ct := e.gcm.Seal(nil, nonce, e.buf, aad)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
	if _, err := e.wc.Write(lenBuf[:]); err != nil {
		e.err = err
		return err
	}
	if _, err := e.wc.Write(ct); err != nil {
		e.err = err
		return err
	}
	e.frameIdx++
	e.buf = e.buf[:0]
	return nil
}

// Close flushes any pending partial frame, writes the authenticated final frame
// (empty plaintext, is_final=1, frame_index=total data frames) and closes the
// underlying file. Idempotent. On a prior write error it still closes the file
// but writes no final frame (the stream is already broken).
func (e *EncWriter) Close() error {
	if e.closed {
		return e.err
	}
	e.closed = true
	if e.err != nil {
		_ = e.wc.Close()
		return e.err
	}
	if len(e.buf) > 0 {
		if err := e.flushFrame(false); err != nil {
			_ = e.wc.Close()
			return e.err
		}
	}
	if err := e.flushFrame(true); err != nil {
		_ = e.wc.Close()
		return e.err
	}
	if err := e.wc.Close(); err != nil && e.err == nil {
		e.err = err
	}
	return e.err
}

// Err reports the latched write/close error.
func (e *EncWriter) Err() error { return e.err }

// DecReader decrypts a framed AEAD file into its plaintext stream. Read serves
// decrypted bytes; frames are opened on demand with a reader-tracked, strictly
// increasing frame index so any reorder/replay/cross-file substitution fails
// authentication. EOF before the authenticated final frame, or any bytes after
// it, is corruption.
type DecReader struct {
	r         io.Reader
	c         io.Closer // optional: closed by Close()
	gcm       cipher.AEAD
	fileID    []byte
	frameIdx  uint64
	plaintext []byte
	off       int
	done      bool
	err       error
}

// newDecReader reads and validates the file header, derives the per-file key and
// returns a reader positioned at the first frame.
func newDecReader(r io.Reader, masterKey []byte) (*DecReader, error) {
	hdr := make([]byte, castHeaderLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("castrec: read header: %w", err)
	}
	if string(hdr[:4]) != castMagic {
		return nil, errBadMagic
	}
	if hdr[4] != byte(castFormatVer) {
		return nil, errUnsupportedVersion
	}
	fileID := make([]byte, castFileIDLen)
	copy(fileID, hdr[5:5+castFileIDLen])
	fk, err := deriveFileKey(masterKey, fileID)
	if err != nil {
		return nil, fmt.Errorf("castrec: derive file key: %w", err)
	}
	gcm, err := newGCM(fk)
	if err != nil {
		return nil, err
	}
	return &DecReader{r: r, gcm: gcm, fileID: fileID}, nil
}

// Read yields decrypted plaintext, decrypting frames as needed. It returns io.EOF
// only after the authenticated final frame has been consumed and the file is at
// physical EOF.
func (d *DecReader) Read(p []byte) (int, error) {
	if d.err != nil {
		return 0, d.err
	}
	for d.off >= len(d.plaintext) {
		if d.done {
			return 0, io.EOF
		}
		if err := d.nextFrame(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.plaintext[d.off:])
	d.off += n
	return n, nil
}

// nextFrame reads, authenticates and decrypts the next frame. is_final and
// plaintext_len are recovered from the ciphertext length (ptLen = ct_len - tag),
// so a data frame (ptLen>0) authenticates as is_final=0 and the empty final frame
// (ptLen==0) authenticates as is_final=1 — any tampering flips the AAD and fails
// the tag.
func (d *DecReader) nextFrame() error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(d.r, lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			// Clean EOF at a frame boundary means no authenticated final frame was
			// seen -> the file was truncated.
			return errMissingFinal
		}
		return fmt.Errorf("castrec: read frame length: %w", err)
	}
	ctLen := binary.BigEndian.Uint32(lenBuf[:])
	tag := uint32(d.gcm.Overhead())
	if ctLen < tag {
		return errFrameCorrupt
	}
	if ctLen > castChunkSize+tag {
		return errFrameTooLarge
	}
	ct := make([]byte, ctLen)
	if _, err := io.ReadFull(d.r, ct); err != nil {
		return fmt.Errorf("castrec: read frame body: %w", err)
	}
	ptLen := ctLen - tag
	isFinal := ptLen == 0
	nonce := castNonce(d.frameIdx)
	aad := castAAD(d.fileID, d.frameIdx, isFinal, ptLen)
	pt, err := d.gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return fmt.Errorf("castrec: frame %d authentication failed: %w", d.frameIdx, err)
	}
	d.frameIdx++
	if isFinal {
		// The authenticated final frame MUST be the physical end of the file.
		var probe [1]byte
		if _, perr := io.ReadFull(d.r, probe[:]); perr != io.EOF {
			return errTrailingAfterFinal
		}
		d.done = true
		d.plaintext = nil
		d.off = 0
		return nil
	}
	d.plaintext = pt
	d.off = 0
	return nil
}

// Close closes the optional underlying file. Safe to call once.
func (d *DecReader) Close() error {
	if d.c != nil {
		return d.c.Close()
	}
	return nil
}
