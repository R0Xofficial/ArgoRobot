package core

import (
	"bytes"
	"image"
	"image/draw"
	_ "image/jpeg" // the formats Telegram sends pictures in
	"image/png"
	"math"

	_ "golang.org/x/image/webp"

	xdraw "golang.org/x/image/draw"
)

// StickerSide is the length Telegram wants the longest side of a still sticker to have.
const StickerSide = 512

// StillPNG decodes a picture (PNG, JPEG or WebP) and encodes it as PNG with an alpha channel. With fit it is scaled so
// that its longest side is StickerSide pixels.
func StillPNG(data []byte, fit bool) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	var out image.Image = rgba
	if fit && b.Dx() > 0 && b.Dy() > 0 {
		scale := float64(StickerSide) / math.Max(float64(b.Dx()), float64(b.Dy()))
		w := max(1, int(math.Round(float64(b.Dx())*scale)))
		h := max(1, int(math.Round(float64(b.Dy())*scale)))
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), rgba, rgba.Bounds(), xdraw.Src, nil)
		out = dst
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
