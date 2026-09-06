package media

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Size is a frame size in pixels.
type Size struct{ W, H int }

// Probe returns the video resolution of the RTSP source (before rotation).
func Probe(ctx context.Context, ffprobe, url string) (Size, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	args := []string{"-v", "error"}
	if isRTSP(url) {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args,
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "csv=p=0:s=x",
		url,
	)
	cmd := exec.CommandContext(ctx, ffprobe, args...)
	out, err := cmd.Output()
	if err != nil {
		return Size{}, fmt.Errorf("ffprobe: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(parts) != 2 {
		return Size{}, fmt.Errorf("ffprobe: unexpected output %q", string(out))
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return Size{}, fmt.Errorf("ffprobe: bad size %q", string(out))
	}
	return Size{W: w, H: h}, nil
}

// Rotate applies a 90/180 rotation to a size.
func (s Size) Rotate(mode string) Size {
	switch mode {
	case "90cw", "90ccw":
		return Size{W: s.H, H: s.W}
	default:
		return s
	}
}

// isRTSP reports whether u is an rtsp(s) URL (so the tcp transport flag applies).
func isRTSP(u string) bool {
	return strings.HasPrefix(strings.ToLower(u), "rtsp://") ||
		strings.HasPrefix(strings.ToLower(u), "rtsps://")
}

// TransposeFilter returns the ffmpeg -vf transpose expression for a rotation
// mode (none|90cw|90ccw|180), or "" for none.
func TransposeFilter(rotate string) string {
	switch rotate {
	case "90cw":
		return "transpose=1"
	case "90ccw":
		return "transpose=2"
	case "180":
		return "transpose=1,transpose=1"
	default:
		return ""
	}
}

// Letterbox holds the scale/pad used to fit a WxH frame into a square SxS.
type Letterbox struct {
	Src   Size // rotated source size
	S     int  // square model input size
	Scale float64
	NewW  int
	NewH  int
	PadX  int
	PadY  int
}

func NewLetterbox(src Size, s int) Letterbox {
	scale := math.Min(float64(s)/float64(src.W), float64(s)/float64(src.H))
	nw := int(math.Round(float64(src.W) * scale))
	nh := int(math.Round(float64(src.H) * scale))
	return Letterbox{
		Src:   src,
		S:     s,
		Scale: scale,
		NewW:  nw,
		NewH:  nh,
		PadX:  (s - nw) / 2,
		PadY:  (s - nh) / 2,
	}
}

// ToSource maps a box in model (letterboxed) pixel space back to normalized
// coordinates (0..1) of the rotated source frame.
func (l Letterbox) ToSource(x1, y1, x2, y2 float32) (nx1, ny1, nx2, ny2 float32) {
	un := func(v float32, pad int) float32 {
		return (v - float32(pad)) / float32(l.Scale)
	}
	sx1 := un(x1, l.PadX) / float32(l.Src.W)
	sy1 := un(y1, l.PadY) / float32(l.Src.H)
	sx2 := un(x2, l.PadX) / float32(l.Src.W)
	sy2 := un(y2, l.PadY) / float32(l.Src.H)
	clamp := func(v float32) float32 {
		if v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	return clamp(sx1), clamp(sy1), clamp(sx2), clamp(sy2)
}
