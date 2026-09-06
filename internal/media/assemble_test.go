package media

import (
	"image/color"
	"strings"
	"testing"
	"time"
)

func TestOverlayFilters(t *testing.T) {
	a := &Assembler{canBox: true, canText: true, font: "/x.ttf"}
	start := time.Unix(1000, 0)
	ovl := []Overlay{{
		T: start.Add(500 * time.Millisecond),
		Boxes: []OverlayBox{{
			NX1: 0.1, NY1: 0.2, NX2: 0.5, NY2: 0.7,
			Label: "person 87%", Color: color.RGBA{R: 0x33, G: 0xCC, B: 0x88, A: 255},
		}},
	}}

	box := a.overlayFilters(ovl, start, false)
	if len(box) != 1 || !strings.HasPrefix(box[0], "drawbox=") {
		t.Fatalf("want one drawbox, got %v", box)
	}
	if !strings.Contains(box[0], "0x33CC88") || !strings.Contains(box[0], "between(t,0.32,0.68)") {
		t.Fatalf("bad drawbox expr: %s", box[0])
	}

	full := a.overlayFilters(ovl, start, true)
	if len(full) != 2 || !strings.HasPrefix(full[1], "drawtext=") {
		t.Fatalf("want drawbox+drawtext, got %v", full)
	}
	if strings.Contains(full[1], "text='person 87%'") == false {
		t.Fatalf("caption missing: %s", full[1])
	}

	a.canText = false
	if got := a.overlayFilters(ovl, start, true); got != nil {
		t.Fatalf("no-text ffmpeg should yield nil for withText, got %v", got)
	}
	a.canBox = false
	if got := a.overlayFilters(ovl, start, false); got != nil {
		t.Fatalf("no-drawbox ffmpeg should yield nil, got %v", got)
	}
}

func TestSanitizeText(t *testing.T) {
	if got := sanitizeText("person 87% :;'\"drop"); got != "person 87% drop" {
		t.Fatalf("got %q", got)
	}
}
