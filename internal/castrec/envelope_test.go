package castrec

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// nopWC adapts a *bytes.Buffer to io.WriteCloser (Close is a no-op) so EncWriter
// can seal frames into an in-memory buffer during tests.
type nopWC struct{ *bytes.Buffer }

func (nopWC) Close() error { return nil }

func testMaster(t *testing.T) []byte {
	t.Helper()
	mk, err := deriveMaster(bytes.Repeat([]byte{0xA5}, 32))
	if err != nil {
		t.Fatalf("deriveMaster: %v", err)
	}
	return mk
}

// encEnvelope seals the given plaintext chunks into a framed AEAD byte stream.
func encEnvelope(t *testing.T, mk []byte, chunks [][]byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	ew, err := newEncWriter(nopWC{buf}, mk)
	if err != nil {
		t.Fatalf("newEncWriter: %v", err)
	}
	for _, c := range chunks {
		if _, err := ew.Write(c); err != nil {
			t.Fatalf("EncWriter.Write: %v", err)
		}
	}
	if err := ew.Close(); err != nil {
		t.Fatalf("EncWriter.Close: %v", err)
	}
	return buf.Bytes()
}

// decEnvelope decrypts a framed AEAD byte stream to plaintext.
func decEnvelope(t *testing.T, mk, data []byte) ([]byte, error) {
	t.Helper()
	dr, err := newDecReader(bytes.NewReader(data), mk)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(dr)
}

// splitFrames parses a framed AEAD stream into (header, frames) where each frame
// carries its own 4-byte length prefix, so tests can drop/reorder/replace frames.
func splitFrames(t *testing.T, data []byte) ([]byte, [][]byte) {
	t.Helper()
	if len(data) < castHeaderLen {
		t.Fatalf("data too short for header: %d", len(data))
	}
	hdr := append([]byte(nil), data[:castHeaderLen]...)
	rest := data[castHeaderLen:]
	var frames [][]byte
	for len(rest) > 0 {
		if len(rest) < 4 {
			t.Fatalf("dangling frame length prefix")
		}
		ctLen := int(binary.BigEndian.Uint32(rest[:4]))
		end := 4 + ctLen
		if end > len(rest) {
			t.Fatalf("frame overruns data")
		}
		frames = append(frames, append([]byte(nil), rest[:end]...))
		rest = rest[end:]
	}
	return hdr, frames
}

func joinFrames(hdr []byte, frames [][]byte) []byte {
	out := append([]byte(nil), hdr...)
	for _, f := range frames {
		out = append(out, f...)
	}
	return out
}

// --- test vectors -----------------------------------------------------------

// encrypted round-trip across multiple frames.
func TestEnvelopeRoundTrip(t *testing.T) {
	mk := testMaster(t)
	plain := make([]byte, 40000) // spans 3 frames: 16384 + 16384 + 7232
	for i := range plain {
		plain[i] = byte(i % 251)
	}
	data := encEnvelope(t, mk, [][]byte{plain})
	if _, frames := splitFrames(t, data); len(frames) != 4 {
		t.Fatalf("frames = %d, want 4 (3 data + final)", len(frames))
	}
	got, err := decEnvelope(t, mk, data)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %d bytes want %d", len(got), len(plain))
	}
}

// empty session (no output): only header + authenticated final frame, round-trips
// to zero bytes with no error.
func TestEnvelopeEmptySession(t *testing.T) {
	mk := testMaster(t)
	data := encEnvelope(t, mk, nil)
	if _, frames := splitFrames(t, data); len(frames) != 1 {
		t.Fatalf("empty session frames = %d, want 1 (final only)", len(frames))
	}
	got, err := decEnvelope(t, mk, data)
	if err != nil {
		t.Fatalf("decrypt empty session: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty session got %d bytes, want 0", len(got))
	}
}

// truncation: dropping the final frame -> EOF before authenticated final.
func TestEnvelopeTruncateDropFinal(t *testing.T) {
	mk := testMaster(t)
	data := encEnvelope(t, mk, [][]byte{[]byte("hello world")})
	hdr, frames := splitFrames(t, data)
	truncated := joinFrames(hdr, frames[:len(frames)-1]) // drop final
	if _, err := decEnvelope(t, mk, truncated); err == nil {
		t.Fatal("decrypt should fail when the final frame is missing")
	}
}

// truncation: dropping a tail DATA frame shifts the frame index the reader binds
// into AAD, so the (now mis-indexed) final frame fails authentication.
func TestEnvelopeTruncateDropDataFrame(t *testing.T) {
	mk := testMaster(t)
	blk := func(b byte) []byte { return bytes.Repeat([]byte{b}, castChunkSize) }
	data := encEnvelope(t, mk, [][]byte{blk('A'), blk('B'), blk('C')})
	hdr, frames := splitFrames(t, data)
	if len(frames) != 4 {
		t.Fatalf("frames = %d, want 4", len(frames))
	}
	trimmed := [][]byte{frames[0], frames[1], frames[3]} // drop 3rd data frame, keep final
	if _, err := decEnvelope(t, mk, joinFrames(hdr, trimmed)); err == nil {
		t.Fatal("decrypt should fail when a data frame is dropped")
	}
}

// trailing bytes after the authenticated final frame -> corruption.
func TestEnvelopeTrailingAfterFinal(t *testing.T) {
	mk := testMaster(t)
	data := encEnvelope(t, mk, [][]byte{[]byte("payload")})
	tampered := append(append([]byte(nil), data...), 0x00, 0x01, 0x02)
	if _, err := decEnvelope(t, mk, tampered); err == nil {
		t.Fatal("decrypt should fail with trailing bytes after the final frame")
	}
}

// appending an otherwise well-formed frame after the final frame -> corruption.
func TestEnvelopeAppendRawFrameAfterFinal(t *testing.T) {
	mk := testMaster(t)
	data := encEnvelope(t, mk, [][]byte{[]byte("payload")})
	hdr, frames := splitFrames(t, data)
	extended := joinFrames(hdr, append(frames, frames[0])) // re-append first data frame
	if _, err := decEnvelope(t, mk, extended); err == nil {
		t.Fatal("decrypt should fail with an extra frame after the final frame")
	}
}

// reordering data frames -> the AAD frame index no longer matches -> auth fails.
func TestEnvelopeReorderFrames(t *testing.T) {
	mk := testMaster(t)
	blk := func(b byte) []byte { return bytes.Repeat([]byte{b}, castChunkSize) }
	data := encEnvelope(t, mk, [][]byte{blk('A'), blk('B')})
	hdr, frames := splitFrames(t, data)
	frames[0], frames[1] = frames[1], frames[0] // swap the two data frames
	if _, err := decEnvelope(t, mk, joinFrames(hdr, frames)); err == nil {
		t.Fatal("decrypt should fail when data frames are reordered")
	}
}

// cross-file substitution: file A's header (fileID_A) spliced onto file B's frames
// (sealed under fileID_B's key) -> wrong per-file key + AAD -> auth fails.
func TestEnvelopeCrossFileSubstitution(t *testing.T) {
	mk := testMaster(t)
	dataA := encEnvelope(t, mk, [][]byte{[]byte("alpha payload")})
	dataB := encEnvelope(t, mk, [][]byte{[]byte("bravo payload")})
	hdrA, framesA := splitFrames(t, dataA)
	hdrB, framesB := splitFrames(t, dataB)
	if bytes.Equal(hdrA, hdrB) {
		t.Fatal("two files unexpectedly share a fileID header")
	}
	franken := joinFrames(hdrA, framesB)
	if _, err := decEnvelope(t, mk, franken); err == nil {
		t.Fatal("decrypt should fail when frames come from a different file")
	}
	_ = framesA
}

// single-byte tamper inside a frame's ciphertext -> GCM tag verification fails.
func TestEnvelopeSingleByteTamper(t *testing.T) {
	mk := testMaster(t)
	data := encEnvelope(t, mk, [][]byte{[]byte("secret data here")})
	tampered := append([]byte(nil), data...)
	tampered[castHeaderLen+4] ^= 0xFF // first ciphertext byte (after header + len prefix)
	if _, err := decEnvelope(t, mk, tampered); err == nil {
		t.Fatal("decrypt should fail on a single-byte ciphertext tamper")
	}
}

// a different master key (different secret) cannot decrypt.
func TestEnvelopeWrongKey(t *testing.T) {
	mk := testMaster(t)
	data := encEnvelope(t, mk, [][]byte{[]byte("payload")})
	other, err := deriveMaster(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatalf("deriveMaster: %v", err)
	}
	if _, err := decEnvelope(t, other, data); err == nil {
		t.Fatal("decrypt should fail with the wrong master key")
	}
}

// --- asciicast + factory round-trips ---------------------------------------

// parseAsciicast splits raw asciinema v2 bytes into the header and the
// concatenated output ("o") event data.
func parseAsciicast(t *testing.T, raw []byte) (asciicastHeader, string) {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("empty asciicast")
	}
	var hdr asciicastHeader
	if err := json.Unmarshal(lines[0], &hdr); err != nil {
		t.Fatalf("parse header %q: %v", lines[0], err)
	}
	var out bytes.Buffer
	for _, l := range lines[1:] {
		if len(l) == 0 {
			continue
		}
		var ev []json.RawMessage
		if err := json.Unmarshal(l, &ev); err != nil {
			t.Fatalf("parse event %q: %v", l, err)
		}
		if len(ev) != 3 {
			t.Fatalf("event has %d fields, want 3: %q", len(ev), l)
		}
		var typ, data string
		if err := json.Unmarshal(ev[1], &typ); err != nil {
			t.Fatalf("parse event type: %v", err)
		}
		if typ != "o" {
			t.Fatalf("event type = %q, want o", typ)
		}
		if err := json.Unmarshal(ev[2], &data); err != nil {
			t.Fatalf("parse event data: %v", err)
		}
		out.WriteString(data)
	}
	return hdr, out.String()
}

// pinnedClock returns a now func that advances one second per call.
func pinnedClock() func() time.Time {
	base := time.Unix(1_000_000, 0)
	ticks := 0
	return func() time.Time {
		d := time.Duration(ticks) * time.Second
		ticks++
		return base.Add(d)
	}
}

func TestFactoryPlaintextRoundTrip(t *testing.T) {
	rec, err := New(config.CastConfig{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rec.Encrypted() {
		t.Fatal("plaintext recorder must not report Encrypted()")
	}
	rec.now = pinnedClock()
	path := filepath.Join(t.TempDir(), "pty.cast")
	sink, err := rec.Open(path, 120, 40, 1_700_000_000)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, chunk := range []string{"ls -la\r\n", "total 0\r\n"} {
		if _, err := sink.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sink.Err() != nil {
		t.Fatalf("sink.Err() = %v", sink.Err())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cast: %v", err)
	}
	hdr, out := parseAsciicast(t, raw)
	if hdr.Version != 2 || hdr.Width != 120 || hdr.Height != 40 || hdr.Timestamp != 1_700_000_000 {
		t.Fatalf("header = %+v", hdr)
	}
	if out != "ls -la\r\ntotal 0\r\n" {
		t.Fatalf("output = %q", out)
	}
}

func TestFactoryEncryptedRoundTrip(t *testing.T) {
	secret := bytes.Repeat([]byte{0x5A}, 32)
	rec, err := New(config.CastConfig{
		Enabled:    true,
		Encryption: config.CastEncryptionConfig{Enabled: true, KeyEnv: "GOFER_CAST_KEY"},
	}, secret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !rec.Encrypted() {
		t.Fatal("recorder must report Encrypted()")
	}
	rec.now = pinnedClock()
	path := filepath.Join(t.TempDir(), "pty.cast")
	sink, err := rec.Open(path, 0, 0, 1_700_000_000) // 0,0 -> 80x24 fallback
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := "echo secret-token\r\nsecret-token\r\n"
	if _, err := sink.Write([]byte(want)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cast: %v", err)
	}
	if string(raw[:4]) != castMagic {
		t.Fatalf("missing magic, got %q", raw[:4])
	}
	if bytes.Contains(raw, []byte("secret-token")) {
		t.Fatal("plaintext leaked into the encrypted cast file")
	}
	rc, err := rec.NewDecReader(path)
	if err != nil {
		t.Fatalf("NewDecReader: %v", err)
	}
	dec, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("decrypt stream: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close dec reader: %v", err)
	}
	hdr, out := parseAsciicast(t, dec)
	if hdr.Width != defaultCols || hdr.Height != defaultRows {
		t.Fatalf("fallback dims = %dx%d, want %dx%d", hdr.Width, hdr.Height, defaultCols, defaultRows)
	}
	if out != want {
		t.Fatalf("decrypted output = %q, want %q", out, want)
	}
}
