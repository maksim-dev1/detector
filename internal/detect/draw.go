package detect

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// ClassColor returns a stable, visually distinct RGBA color for a COCO class
// index. Golden-ratio hue stepping keeps neighbouring class ids far apart.
func ClassColor(class int) color.RGBA {
	h := math.Mod(float64(class)*0.6180339887498949, 1.0)
	r, g, b := hsvToRGB(h, 0.72, 1.0)
	return color.RGBA{R: r, G: g, B: b, A: 255}
}

// Caption is the per-box label drawn next to a detection, e.g. "person 87%".
func Caption(b Box) string {
	return fmt.Sprintf("%s %.0f%%", b.Label, b.Score*100)
}

// Annotate draws every box on img using its per-class color plus a caption with
// the confidence percentage. Coordinates are in img pixel space (that is, the
// model / letterbox space the boxes come back in).
func Annotate(img *image.RGBA, boxes []Box) {
	for _, b := range boxes {
		c := ClassColor(b.Class)
		x1, y1 := int(b.X1), int(b.Y1)
		x2, y2 := int(b.X2), int(b.Y2)
		strokeRect(img, x1, y1, x2, y2, c, 2)
		drawCaption(img, x1, y1, Caption(b), c)
	}
}

func strokeRect(img *image.RGBA, x1, y1, x2, y2 int, c color.RGBA, th int) {
	if x2 < x1 {
		x1, x2 = x2, x1
	}
	if y2 < y1 {
		y1, y2 = y2, y1
	}
	fill := func(r image.Rectangle) {
		draw.Draw(img, r.Intersect(img.Bounds()), &image.Uniform{c}, image.Point{}, draw.Src)
	}
	fill(image.Rect(x1, y1, x2, y1+th))
	fill(image.Rect(x1, y2-th, x2, y2))
	fill(image.Rect(x1, y1, x1+th, y2))
	fill(image.Rect(x2-th, y1, x2, y2))
}

func drawCaption(img *image.RGBA, x, y int, s string, bg color.RGBA) {
	const h = 15
	w := len(s)*7 + 4
	top := y - h
	if top < 0 {
		top = y // box hugs the frame edge; drop the label inside
	}
	if x < 0 {
		x = 0
	}
	box := image.Rect(x, top, x+w, top+h)
	draw.Draw(img, box.Intersect(img.Bounds()), &image.Uniform{bg}, image.Point{}, draw.Src)

	fg := color.RGBA{0, 0, 0, 255}
	if luma(bg) < 140 {
		fg = color.RGBA{255, 255, 255, 255}
	}
	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{fg},
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x+2, top+11),
	}
	d.DrawString(s)
}

func luma(c color.RGBA) float64 {
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

func hsvToRGB(h, s, v float64) (uint8, uint8, uint8) {
	i := math.Floor(h * 6)
	f := h*6 - i
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	var r, g, b float64
	switch int(i) % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	case 5:
		r, g, b = v, p, q
	}
	return uint8(r * 255), uint8(g * 255), uint8(b * 255)
}
