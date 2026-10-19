package attachment

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// ThumbSide is the longest side of a thumbnail in pixels. thumbMaxPixels bounds what is decoded
// to make one (an RGBA copy costs 4 bytes per pixel, twice): larger images keep their original.
const (
	ThumbSide      = 320
	thumbMaxPixels = 64_000_000
)

// Thumbnail renders a thumbnail of data (longest side ThumbSide, JPEG for a JPEG source, PNG
// otherwise). It never fails the caller: on ANY problem — a WebP (no decoder is linked), a
// corrupt or oversized image, an encoder error — it returns the original bytes and mime with
// ok=false. An image already within ThumbSide is returned as is (ok=true).
func Thumbnail(mime string, data []byte) (out []byte, outMime string, ok bool) {
	if mime == MimeWebP {
		return data, mime, false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxSide || cfg.Height > maxSide ||
		cfg.Width*cfg.Height > thumbMaxPixels {
		return data, mime, false
	}
	if cfg.Width <= ThumbSide && cfg.Height <= ThumbSide {
		return data, mime, true
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime, false
	}
	small := downscale(src, ThumbSide)
	var buf bytes.Buffer
	if mime == MimeJPEG {
		// JPEG has no alpha, so the flattened RGBA is safe to encode as is.
		if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 80}); err != nil {
			return data, mime, false
		}
		return buf.Bytes(), MimeJPEG, true
	}
	if err := png.Encode(&buf, small); err != nil {
		return data, mime, false
	}
	return buf.Bytes(), MimePNG, true
}

// downscale shrinks src so its longest side is side, with a box filter (each output pixel is
// the average of the source rectangle it covers; premultiplied alpha is averaged correctly).
func downscale(src image.Image, side int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := side, side
	if sw >= sh {
		dh = max(1, sh*side/sw)
	} else {
		dw = max(1, sw*side/sh)
	}
	// Normalise to RGBA once so the inner loop reads raw bytes.
	rgba := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := range dw {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy++ {
				row := rgba.Pix[yy*rgba.Stride:]
				for xx := x0; xx < x1; xx++ {
					p := row[xx*4 : xx*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					bl += uint32(p[2])
					a += uint32(p[3])
					n++
				}
			}
			dst.SetRGBA(x, y, color.RGBA{R: uint8(r / n), G: uint8(g / n), B: uint8(bl / n), A: uint8(a / n)})
		}
	}
	return dst
}
