package api

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"github.com/skip2/go-qrcode"
)

// qrlogo.png is the app icon rendered at 120px (see assets/zkaraver.svg).
//
//go:embed qrlogo.png
var qrLogoPNG []byte

const (
	qrSize    = 512
	qrLogoPad = 10 // white margin around the logo
)

// roomQR renders url as a QR code with the app icon in the centre. Highest
// error correction (~30%) leaves plenty of room for the logo, which covers
// well under 10% of the symbol.
func roomQR(url string) ([]byte, error) {
	q, err := qrcode.New(url, qrcode.Highest)
	if err != nil {
		return nil, err
	}
	src := q.Image(qrSize)
	img := image.NewRGBA(src.Bounds())
	draw.Draw(img, img.Bounds(), src, image.Point{}, draw.Src)

	logo, err := png.Decode(bytes.NewReader(qrLogoPNG))
	if err != nil {
		return nil, err
	}
	lb := logo.Bounds()
	at := image.Pt((img.Bounds().Dx()-lb.Dx())/2, (img.Bounds().Dy()-lb.Dy())/2)
	backing := image.Rect(at.X-qrLogoPad, at.Y-qrLogoPad, at.X+lb.Dx()+qrLogoPad, at.Y+lb.Dy()+qrLogoPad)
	fillRoundedRect(img, backing, 30, color.White)
	draw.Draw(img, lb.Add(at).Sub(lb.Min), logo, lb.Min, draw.Over)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fillRoundedRect(img *image.RGBA, r image.Rectangle, radius int, c color.Color) {
	inCorner := func(x, y, cx, cy int) bool {
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy > radius*radius
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			left, right := x < r.Min.X+radius, x >= r.Max.X-radius
			top, bottom := y < r.Min.Y+radius, y >= r.Max.Y-radius
			switch {
			case left && top && inCorner(x, y, r.Min.X+radius, r.Min.Y+radius),
				right && top && inCorner(x, y, r.Max.X-radius-1, r.Min.Y+radius),
				left && bottom && inCorner(x, y, r.Min.X+radius, r.Max.Y-radius-1),
				right && bottom && inCorner(x, y, r.Max.X-radius-1, r.Max.Y-radius-1):
				continue
			}
			img.Set(x, y, c)
		}
	}
}
