package attachment

import (
	"bytes"
	"encoding/binary"
	"image"
	_ "image/gif" // register decoders for image.DecodeConfig / Decode
	_ "image/jpeg"
	_ "image/png"
)

// The four accepted MIME types. Nothing else is stored: in particular SVG (scriptable) and
// HTML are refused whatever the file name or the declared Content-Type says.
const (
	MimePNG  = "image/png"
	MimeJPEG = "image/jpeg"
	MimeGIF  = "image/gif"
	MimeWebP = "image/webp"
)

// maxSide / maxPixels bound what the server is willing to keep (and decode for a thumbnail).
const (
	maxSide   = 16384
	maxPixels = 100_000_000
)

// SniffMime decides the type from the leading bytes only. It returns "" for anything that is
// not PNG, JPEG, GIF or WebP.
func SniffMime(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return MimePNG
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return MimeJPEG
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return MimeGIF
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return MimeWebP
	}
	return ""
}

// Extension is the file extension a sniffed MIME type conventionally has (with the dot).
func Extension(mime string) string {
	switch mime {
	case MimePNG:
		return ".png"
	case MimeJPEG:
		return ".jpg"
	case MimeGIF:
		return ".gif"
	case MimeWebP:
		return ".webp"
	}
	return ""
}

// Dimensions reads the pixel size of an already sniffed image. PNG / JPEG / GIF go through the
// standard library's header decoder; WebP is read from its RIFF header by hand (x/image is not
// a dependency). ok is false when the header is damaged or the size is zero.
func Dimensions(mime string, data []byte) (w, h int, ok bool) {
	if mime == MimeWebP {
		w, h, ok = webpDimensions(data)
	} else {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0, false
		}
		w, h, ok = cfg.Width, cfg.Height, true
	}
	if !ok || w <= 0 || h <= 0 || w > maxSide || h > maxSide || w*h > maxPixels {
		return 0, 0, false
	}
	return w, h, true
}

// webpDimensions parses the first chunk of a RIFF/WEBP file: lossy "VP8 ", lossless "VP8L" or
// extended "VP8X".
func webpDimensions(b []byte) (w, h int, ok bool) {
	if len(b) < 30 || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WEBP")) {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8 ":
		// 3-byte frame tag, 3-byte start code 9d 01 2a, then 14-bit width / height (+ scale bits).
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), true
	case "VP8L":
		if b[20] != 0x2f {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(b[21:25])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, true
	case "VP8X":
		w = int(uint32(b[24])|uint32(b[25])<<8|uint32(b[26])<<16) + 1
		h = int(uint32(b[27])|uint32(b[28])<<8|uint32(b[29])<<16) + 1
		return w, h, true
	}
	return 0, 0, false
}
