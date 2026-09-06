package media

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Clip is an assembled event recording plus a still frame.
type Clip struct {
	VideoPath    string
	SnapshotPath string
	Start        time.Time
	End          time.Time
}

// OverlayBox is one detection rectangle in normalized (0..1) coordinates of the
// upright clip frame, with the caption and color to burn in.
type OverlayBox struct {
	NX1, NY1, NX2, NY2 float32
	Label              string
	Color              color.RGBA
}

// Overlay is the set of detections visible at one instant of the clip.
type Overlay struct {
	T     time.Time
	Boxes []OverlayBox
}

// Assembler stitches ring-buffer segments into a trimmed, upright event clip.
type Assembler struct {
	ffmpeg    string
	src       *Source
	transpose string
	outDir    string

	canBox  bool
	canText bool
	font    string
}

func NewAssembler(ffmpeg string, src *Source, transpose, outDir string) *Assembler {
	a := &Assembler{ffmpeg: ffmpeg, src: src, transpose: transpose, outDir: outDir}
	filters, _ := exec.Command(ffmpeg, "-hide_banner", "-filters").Output()
	a.canBox = strings.Contains(string(filters), " drawbox ")
	a.font = findFont()
	a.canText = a.font != "" && strings.Contains(string(filters), " drawtext ")
	return a
}

// Assemble builds a clip covering [from, to]. It copies the needed segments to
// a temp dir first so the ring buffer can't overwrite them mid-encode. When
// overlays are supplied the detection boxes are burned into the video.
func (a *Assembler) Assemble(ctx context.Context, id string, from, to time.Time, overlays []Overlay) (*Clip, error) {
	segs, err := a.src.Between(from, to)
	if err != nil {
		return nil, fmt.Errorf("select segments: %w", err)
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("no segments available for window")
	}

	tmp, err := os.MkdirTemp("", "clip-"+id+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	listPath := filepath.Join(tmp, "list.txt")
	lf, err := os.Create(listPath)
	if err != nil {
		return nil, err
	}
	for i, s := range segs {
		dst := filepath.Join(tmp, fmt.Sprintf("p%03d.ts", i))
		if err := copyFile(s.Path, dst); err != nil {
			lf.Close()
			return nil, fmt.Errorf("copy segment: %w", err)
		}
		fmt.Fprintf(lf, "file '%s'\n", dst)
	}
	lf.Close()

	// Offset of the requested start within the first copied segment.
	offset := from.Sub(segs[0].Start).Seconds()
	if offset < 0 {
		offset = 0
	}
	dur := to.Sub(from).Seconds()
	if dur <= 0 {
		dur = float64(segs[len(segs)-1].End.Sub(segs[0].Start).Seconds())
	}

	if err := os.MkdirAll(a.outDir, 0o755); err != nil {
		return nil, err
	}
	videoPath := filepath.Join(a.outDir, id+".mp4")
	snapPath := filepath.Join(a.outDir, id+".jpg")

	base := []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-ss", fmt.Sprintf("%.3f", offset),
		"-t", fmt.Sprintf("%.3f", dur),
	}

	// Build the -vf chain: rotation first, then burned-in detection boxes.
	var vf []string
	if a.transpose != "" {
		vf = append(vf, a.transpose)
	}
	// Overlays are timed relative to the clip start (after the -ss trim).
	boxVF := a.overlayFilters(overlays, from.Add(time.Duration(offset*float64(time.Second))), false)
	fullVF := a.overlayFilters(overlays, from.Add(time.Duration(offset*float64(time.Second))), true)

	// Attempt order: boxes+text, then boxes only, then no overlay. Never lose the clip.
	attempts := [][]string{}
	if len(fullVF) > 0 {
		attempts = append(attempts, append(append([]string{}, vf...), fullVF...))
	}
	if len(boxVF) > 0 {
		attempts = append(attempts, append(append([]string{}, vf...), boxVF...))
	}
	attempts = append(attempts, append([]string{}, vf...))

	var lastErr error
	for i, chain := range attempts {
		args := append([]string{}, base...)
		if len(chain) > 0 {
			args = append(args, "-vf", strings.Join(chain, ","),
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "23")
		} else {
			args = append(args, "-c", "copy")
		}
		args = append(args, "-movflags", "+faststart", "-y", videoPath)

		out, err := exec.CommandContext(ctx, a.ffmpeg, args...).CombinedOutput()
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("ffmpeg assemble (attempt %d): %w: %s", i+1, err, out)
	}
	if lastErr != nil {
		return nil, lastErr
	}

	// Still frame from the middle of the clip.
	snapArgs := []string{
		"-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", dur/2),
		"-i", videoPath,
		"-frames:v", "1", "-q:v", "3", "-y", snapPath,
	}
	if out, err := exec.CommandContext(ctx, a.ffmpeg, snapArgs...).CombinedOutput(); err != nil {
		// non-fatal: keep the video
		snapPath = ""
		_ = out
	}

	return &Clip{VideoPath: videoPath, SnapshotPath: snapPath, Start: from, End: to}, nil
}

const maxOverlaySamples = 160

// overlayFilters turns timed detections into drawbox (+drawtext) filter entries.
// clipStart is the wall-clock time of the first visible frame. withText adds the
// caption labels (skipped when no usable font was found).
func (a *Assembler) overlayFilters(overlays []Overlay, clipStart time.Time, withText bool) []string {
	if len(overlays) == 0 || !a.canBox {
		return nil
	}
	if withText && !a.canText {
		return nil
	}

	stride := 1
	if len(overlays) > maxOverlaySamples {
		stride = len(overlays)/maxOverlaySamples + 1
	}
	half := 0.18 * float64(stride)

	var f []string
	for i := 0; i < len(overlays); i += stride {
		o := overlays[i]
		rel := o.T.Sub(clipStart).Seconds()
		if rel < -1 {
			continue
		}
		en := fmt.Sprintf("between(t,%.2f,%.2f)", rel-half, rel+half)
		for _, b := range o.Boxes {
			w := b.NX2 - b.NX1
			h := b.NY2 - b.NY1
			if w <= 0 || h <= 0 {
				continue
			}
			hex := fmt.Sprintf("0x%02X%02X%02X", b.Color.R, b.Color.G, b.Color.B)
			f = append(f, fmt.Sprintf(
				"drawbox=x='iw*%.4f':y='ih*%.4f':w='iw*%.4f':h='ih*%.4f':color=%s:thickness=3:enable='%s'",
				b.NX1, b.NY1, w, h, hex, en))
			if withText {
				y := fmt.Sprintf("if(lt(ih*%.4f-22\\,0)\\,ih*%.4f+2\\,ih*%.4f-22)", b.NY1, b.NY1, b.NY1)
				f = append(f, fmt.Sprintf(
					"drawtext=fontfile='%s':text='%s':expansion=none:fontsize=18:fontcolor=white:box=1:boxcolor=%s@0.75:boxborderw=4:x='iw*%.4f':y='%s':enable='%s'",
					a.font, sanitizeText(b.Label), hex, b.NX1, y, en))
			}
		}
	}
	return f
}

func sanitizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '%', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

var fontCandidates = []string{
	"/System/Library/Fonts/Supplemental/Arial.ttf",
	"/System/Library/Fonts/Supplemental/Verdana.ttf",
	"/Library/Fonts/Arial.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
	"/usr/share/fonts/TTF/DejaVuSans.ttf",
	"/usr/share/fonts/dejavu/DejaVuSans.ttf",
}

func findFont() string {
	for _, p := range fontCandidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}
