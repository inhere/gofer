package attachment

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	assert.NoErr(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func jpegBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	assert.NoErr(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil))
	return buf.Bytes()
}

func gifBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	assert.NoErr(t, gif.Encode(&buf, img, nil))
	return buf.Bytes()
}

// webpBytes builds the first bytes of a WebP file of the given chunk kind: enough for the
// header parser (the pixel data is not decodable, which is what the thumbnail fallback needs).
func webpBytes(kind string, w, h int) []byte {
	b := make([]byte, 40)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 32)
	copy(b[8:], "WEBP")
	copy(b[12:], kind)
	switch kind {
	case "VP8 ":
		b[23], b[24], b[25] = 0x9d, 0x01, 0x2a
		binary.LittleEndian.PutUint16(b[26:], uint16(w))
		binary.LittleEndian.PutUint16(b[28:], uint16(h))
	case "VP8L":
		b[20] = 0x2f
		binary.LittleEndian.PutUint32(b[21:], uint32(w-1)|uint32(h-1)<<14)
	case "VP8X":
		for i := range 3 {
			b[24+i] = byte((w - 1) >> (8 * i))
			b[27+i] = byte((h - 1) >> (8 * i))
		}
	}
	return b
}

func TestSniffMime(t *testing.T) {
	assert.Eq(t, MimePNG, SniffMime(pngBytes(t, 2, 2)))
	assert.Eq(t, MimeJPEG, SniffMime(jpegBytes(t, 2, 2)))
	assert.Eq(t, MimeGIF, SniffMime(gifBytes(t, 2, 2)))
	assert.Eq(t, MimeWebP, SniffMime(webpBytes("VP8L", 4, 4)))
	for name, data := range map[string][]byte{
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":  []byte("<!doctype html><html></html>"),
		"text":  []byte("hello"),
		"empty": nil,
		"riff":  []byte("RIFF....WAVEfmt "),
	} {
		assert.Eq(t, "", SniffMime(data), name)
	}
}

func TestDimensions(t *testing.T) {
	for _, tc := range []struct {
		name, mime string
		data       []byte
		w, h       int
	}{
		{"png", MimePNG, pngBytes(t, 7, 5), 7, 5},
		{"jpeg", MimeJPEG, jpegBytes(t, 9, 3), 9, 3},
		{"gif", MimeGIF, gifBytes(t, 4, 6), 4, 6},
		{"webp lossy", MimeWebP, webpBytes("VP8 ", 640, 480), 640, 480},
		{"webp lossless", MimeWebP, webpBytes("VP8L", 1000, 20), 1000, 20},
		{"webp extended", MimeWebP, webpBytes("VP8X", 4000, 3000), 4000, 3000},
	} {
		w, h, ok := Dimensions(tc.mime, tc.data)
		assert.True(t, ok, tc.name)
		assert.Eq(t, tc.w, w, tc.name)
		assert.Eq(t, tc.h, h, tc.name)
	}
	_, _, ok := Dimensions(MimePNG, []byte("\x89PNG\r\n\x1a\ntruncated"))
	assert.False(t, ok)
	_, _, ok = Dimensions(MimeWebP, webpBytes("VP8X", 20000, 20))
	assert.False(t, ok, "side beyond maxSide")
}

func TestThumbnail(t *testing.T) {
	t.Run("png is scaled to the longest side", func(t *testing.T) {
		out, mime, ok := Thumbnail(MimePNG, pngBytes(t, 800, 400))
		assert.True(t, ok)
		assert.Eq(t, MimePNG, mime)
		cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
		assert.NoErr(t, err)
		assert.Eq(t, ThumbSide, cfg.Width)
		assert.Eq(t, 160, cfg.Height)
	})
	t.Run("portrait jpeg stays jpeg", func(t *testing.T) {
		out, mime, ok := Thumbnail(MimeJPEG, jpegBytes(t, 300, 1200))
		assert.True(t, ok)
		assert.Eq(t, MimeJPEG, mime)
		cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
		assert.NoErr(t, err)
		assert.Eq(t, "jpeg", format)
		assert.Eq(t, 80, cfg.Width)
		assert.Eq(t, ThumbSide, cfg.Height)
	})
	t.Run("gif becomes a png", func(t *testing.T) {
		_, mime, ok := Thumbnail(MimeGIF, gifBytes(t, 640, 640))
		assert.True(t, ok)
		assert.Eq(t, MimePNG, mime)
	})
	t.Run("small image is returned as is", func(t *testing.T) {
		in := pngBytes(t, 100, 50)
		out, mime, ok := Thumbnail(MimePNG, in)
		assert.True(t, ok)
		assert.Eq(t, MimePNG, mime)
		assert.True(t, bytes.Equal(in, out))
	})
	t.Run("webp falls back to the original", func(t *testing.T) {
		in := webpBytes("VP8L", 2000, 2000)
		out, mime, ok := Thumbnail(MimeWebP, in)
		assert.False(t, ok)
		assert.Eq(t, MimeWebP, mime)
		assert.True(t, bytes.Equal(in, out))
	})
	t.Run("corrupt body falls back to the original", func(t *testing.T) {
		in := append(pngBytes(t, 800, 800)[:200], bytes.Repeat([]byte{0}, 64)...)
		out, mime, ok := Thumbnail(MimePNG, in)
		assert.False(t, ok)
		assert.Eq(t, MimePNG, mime)
		assert.True(t, bytes.Equal(in, out))
	})
}

func newTestService(t *testing.T, lim Limits) (*Service, *jobstore.Store) {
	t.Helper()
	st, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	assert.NoErr(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return New(t.TempDir(), st, func() Limits { return lim }), st
}

func TestSaveStoresFileAndRow(t *testing.T) {
	svc, st := newTestService(t, Limits{MaxBytes: 1 << 20, HourlyPerCaller: 100, Retention: 24 * time.Hour})
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })

	// A PNG whose file name claims .jpg is judged by its bytes: stored as a PNG.
	data := pngBytes(t, 30, 20)
	rec, err := svc.Save("alice", bytes.NewReader(data))
	assert.NoErr(t, err)
	assert.Eq(t, MimePNG, rec.Mime)
	assert.Eq(t, 30, rec.Width)
	assert.Eq(t, 20, rec.Height)
	assert.Eq(t, int64(len(data)), rec.Bytes)
	assert.Eq(t, now.Add(24*time.Hour).Unix(), rec.ExpiresAt)
	assert.True(t, ValidID(rec.ID))
	assert.Eq(t, "20261019/"+rec.ID, rec.Path)

	onDisk, err := os.ReadFile(filepath.Join(svc.Root(), "20261019", rec.ID))
	assert.NoErr(t, err)
	assert.True(t, bytes.Equal(data, onDisk))

	got, ok, err := st.GetAttachment(rec.ID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", got.Caller)
	assert.Eq(t, "", got.ThreadID)

	// Negative retention is carried as "keep forever".
	svc2, _ := newTestService(t, Limits{MaxBytes: 1 << 20, Retention: 0})
	rec2, err := svc2.Save("alice", bytes.NewReader(data))
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), rec2.ExpiresAt)
}

func TestSaveRefusals(t *testing.T) {
	svc, _ := newTestService(t, Limits{MaxBytes: 2048, HourlyPerCaller: 3})
	_, err := svc.Save("a", strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	assert.True(t, errors.Is(err, ErrUnsupportedType))
	_, err = svc.Save("a", bytes.NewReader(append(pngBytes(t, 10, 10), bytes.Repeat([]byte{1}, 4096)...)))
	assert.True(t, errors.Is(err, ErrTooLarge))
	// Third attempt is still inside the hourly budget (3), the fourth is not.
	_, err = svc.Save("a", strings.NewReader("plain text"))
	assert.True(t, errors.Is(err, ErrUnsupportedType))
	_, err = svc.Save("a", bytes.NewReader(pngBytes(t, 4, 4)))
	assert.True(t, errors.Is(err, ErrRateLimited))
	// The budget is per caller and rolls with time.
	_, err = svc.Save("b", bytes.NewReader(pngBytes(t, 4, 4)))
	assert.NoErr(t, err)
	svc.SetClock(func() time.Time { return time.Now().Add(61 * time.Minute) })
	_, err = svc.Save("a", bytes.NewReader(pngBytes(t, 4, 4)))
	assert.NoErr(t, err)
}

func TestReadThumbAndDeleteUnsent(t *testing.T) {
	svc, st := newTestService(t, Limits{MaxBytes: 4 << 20})
	rec, err := svc.Save("alice", bytes.NewReader(pngBytes(t, 640, 480)))
	assert.NoErr(t, err)
	th, mime, err := svc.ReadThumb(rec)
	assert.NoErr(t, err)
	assert.Eq(t, MimePNG, mime)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(th))
	assert.NoErr(t, err)
	assert.Eq(t, ThumbSide, cfg.Width)

	assert.NoErr(t, svc.DeleteUnsent(rec))
	_, err = os.Stat(filepath.Join(svc.Root(), filepath.FromSlash(rec.Path)))
	assert.True(t, os.IsNotExist(err))
	_, ok, _ := svc.Get(rec.ID)
	assert.False(t, ok)

	rec2, err := svc.Save("alice", bytes.NewReader(pngBytes(t, 8, 8)))
	assert.NoErr(t, err)
	assert.NoErr(t, st.BindAttachment(rec2.ID, "j:x", "job", "x"))
	assert.True(t, errors.Is(svc.DeleteUnsent(rec2), jobstore.ErrAttachmentBound))
	_, err = svc.Read(rec2)
	assert.NoErr(t, err)

	// Path traversal ids never reach the file system.
	_, ok, err = svc.Get("../../etc/passwd")
	assert.NoErr(t, err)
	assert.False(t, ok)
}

func TestPruneRemovesExpiredRowsAndFiles(t *testing.T) {
	svc, st := newTestService(t, Limits{MaxBytes: 1 << 20, Retention: time.Hour})
	base := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return base })
	old, err := svc.Save("alice", bytes.NewReader(pngBytes(t, 8, 8)))
	assert.NoErr(t, err)
	// A SENT attachment expires like any other.
	oldSent, err := svc.Save("alice", bytes.NewReader(pngBytes(t, 8, 8)))
	assert.NoErr(t, err)
	assert.NoErr(t, st.BindAttachment(oldSent.ID, "j:x", "job", "x"))

	svc.SetClock(func() time.Time { return base.Add(48 * time.Hour) })
	fresh, err := svc.Save("alice", bytes.NewReader(pngBytes(t, 8, 8)))
	assert.NoErr(t, err)

	// A keep-forever one (expires_at 0) from the old day.
	forever := jobstore.Attachment{ID: "forever0000000000000", Caller: "alice", Mime: MimePNG, Bytes: 1,
		CreatedAt: base.Unix(), Path: "20261001/forever0000000000000"}
	assert.NoErr(t, st.InsertAttachment(forever))
	assert.NoErr(t, os.WriteFile(filepath.Join(svc.Root(), "20261001", "forever0000000000000"), []byte("x"), 0o600))

	n, err := svc.Prune()
	assert.NoErr(t, err)
	assert.Eq(t, 2, n)
	for _, r := range []jobstore.Attachment{old, oldSent} {
		_, ok, _ := st.GetAttachment(r.ID)
		assert.False(t, ok)
		_, err := os.Stat(filepath.Join(svc.Root(), filepath.FromSlash(r.Path)))
		assert.True(t, os.IsNotExist(err))
	}
	for _, r := range []jobstore.Attachment{fresh, forever} {
		_, ok, _ := st.GetAttachment(r.ID)
		assert.True(t, ok, r.ID)
		_, err := os.Stat(filepath.Join(svc.Root(), filepath.FromSlash(r.Path)))
		assert.NoErr(t, err)
	}
	n, err = svc.Prune()
	assert.NoErr(t, err)
	assert.Eq(t, 0, n)
}
