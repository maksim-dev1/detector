// Package pipeline runs the full detection stack for one video source: decode
// letterboxed frames, run YOLO, debounce detections into events, assemble a
// clip with burned-in boxes, and hand a finished report to a callback.
//
// A Pipeline owns no Telegram/HTTP/DVRIP state — those are wired by the caller
// through Deps so the same pipeline serves the single-camera binary and the
// multi-stream bot service.
package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/maksim-dev1/detector/internal/detect"
	"github.com/maksim-dev1/detector/internal/event"
	"github.com/maksim-dev1/detector/internal/media"
)

// Config is the immutable setup for a pipeline. Rotate and camera/model paths
// can't change without a restart; see Runtime for the mutable subset.
type Config struct {
	Name      string // human label, shown in reports
	RTSPURL   string
	Rotate    string // none|90cw|90ccw|180
	FFmpeg    string
	FFprobe   string
	PublicURL string // base for building clip/snapshot URLs

	Detect    DetectConfig
	Event     EventConfig
	Recording RecordingConfig
}

type DetectConfig struct {
	YoloURL       string
	InputSize     int
	FPS           int
	ConfThreshold float32
	IOUThreshold  float32
	Classes       []string
}

type EventConfig struct {
	TriggerFrames   int
	CooldownSeconds int
	MinEventSeconds int
	PreSeconds      int
	PostSeconds     int
	MaxEventSeconds int
}

type RecordingConfig struct {
	Enabled        bool
	Dir            string // per-stream base dir; segments/ and events/ live under it
	SegmentSeconds int
	RingSegments   int
	RetentionHours int
}

// Runtime is the subset of settings that can change while the pipeline runs.
type Runtime struct {
	ConfThreshold   float32
	Classes         []string
	TriggerFrames   int
	CooldownSeconds int
	PreSeconds      int
	PostSeconds     int
	RecordingOn     bool
	NotifyOn        bool
}

// ClassStat is a per-label summary over an event window.
type ClassStat struct {
	Label    string
	Count    int     // frames the label appeared in
	MaxScore float32 // peak confidence
}

// EventReport is a finished, debounced detection window.
type EventReport struct {
	ID         string
	Name       string
	StartedAt  time.Time
	EndedAt    time.Time
	Classes    []string
	ClassStats []ClassStat
	NotifyOn   bool // caller decides whether to actually send

	VideoPath    string
	SnapshotPath string
	ClipURL      string
	SnapshotURL  string
}

// Metrics receives per-frame detection outcomes (implemented by server.Stats).
type Metrics interface {
	Frame(hasDet bool, labels []string)
}

// Deps are the side-channels a pipeline talks to.
type Deps struct {
	OnPreview func(jpeg []byte) // latest annotated frame (may be called at FPS)
	OnEvent   func(EventReport) // finished event; caller notifies/records
	Metrics   Metrics           // optional
	Logf      func(string, ...any)
}

// Pipeline is one source's detection stack. Create with New, drive with Run.
type Pipeline struct {
	cfg  Config
	deps Deps

	mu      sync.RWMutex
	rt      Runtime
	preview []byte

	yolo *detect.YOLO
	eng  *event.Engine
	src  *media.Source
	asm  *media.Assembler
	lb   media.Letterbox

	dlog  *detLog
	snaps *snapRing
}

func New(cfg Config, rt Runtime, deps Deps) *Pipeline {
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	return &Pipeline{cfg: cfg, deps: deps, rt: rt}
}

// Reconfigure applies a new mutable settings set to the live components.
func (p *Pipeline) Reconfigure(rt Runtime) {
	p.mu.Lock()
	p.rt = rt
	p.mu.Unlock()
	if p.yolo != nil {
		p.yolo.SetConf(rt.ConfThreshold)
		p.yolo.SetClasses(rt.Classes)
	}
	if p.eng != nil {
		p.eng.SetParams(event.Config{
			TriggerFrames: rt.TriggerFrames,
			Cooldown:      time.Duration(rt.CooldownSeconds) * time.Second,
			MinDuration:   time.Duration(p.cfg.Event.MinEventSeconds) * time.Second,
			MaxDuration:   time.Duration(p.cfg.Event.MaxEventSeconds) * time.Second,
		})
	}
}

func (p *Pipeline) runtime() Runtime {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rt
}

// Snapshot returns the most recent annotated preview JPEG, or nil.
func (p *Pipeline) Snapshot() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.preview
}

// Run blocks until ctx is cancelled. It probes the source (retrying on
// failure), then runs the decode+detect loop, restarting ffmpeg as needed.
func (p *Pipeline) Run(ctx context.Context) error {
	srcSize, err := p.probe(ctx)
	if err != nil {
		return err
	}
	rotated := srcSize.Rotate(p.cfg.Rotate)
	p.lb = media.NewLetterbox(rotated, p.cfg.Detect.InputSize)
	p.logf("source %dx%d, rotated %dx%d, model %d, scale %.3f",
		srcSize.W, srcSize.H, rotated.W, rotated.H, p.lb.S, p.lb.Scale)

	rt := p.runtime()

	p.yolo, err = detect.New(detect.Config{
		YoloURL:       p.cfg.Detect.YoloURL,
		InputSize:     p.cfg.Detect.InputSize,
		ConfThreshold: rt.ConfThreshold,
		IOUThreshold:  p.cfg.Detect.IOUThreshold,
		Classes:       rt.Classes,
	})
	if err != nil {
		return fmt.Errorf("detector: %w", err)
	}
	defer p.yolo.Close()

	segDir := filepath.Join(p.cfg.Recording.Dir, "segments")
	eventsDir := filepath.Join(p.cfg.Recording.Dir, "events")
	transpose := media.TransposeFilter(p.cfg.Rotate)

	srcCfg := media.SourceConfig{
		FFmpeg:    p.cfg.FFmpeg,
		URL:       p.cfg.RTSPURL,
		Transpose: transpose,
		Letterbox: p.lb,
		FPS:       p.cfg.Detect.FPS,
	}
	if p.cfg.Recording.Enabled {
		srcCfg.RecordDir = segDir
		srcCfg.SegmentSeconds = p.cfg.Recording.SegmentSeconds
		srcCfg.RingSegments = p.cfg.Recording.RingSegments
	}
	p.src = media.NewSource(srcCfg)
	if p.cfg.Recording.Enabled {
		p.asm = media.NewAssembler(p.cfg.FFmpeg, p.src, transpose, eventsDir)
	}

	histTTL := time.Duration(p.cfg.Event.MaxEventSeconds+p.cfg.Event.PreSeconds+
		p.cfg.Event.PostSeconds+60) * time.Second
	p.dlog = newDetLog(histTTL)
	p.snaps = newSnapRing(24)

	p.eng = event.New(event.Config{
		TriggerFrames: rt.TriggerFrames,
		Cooldown:      time.Duration(rt.CooldownSeconds) * time.Second,
		MinDuration:   time.Duration(p.cfg.Event.MinEventSeconds) * time.Second,
		MaxDuration:   time.Duration(p.cfg.Event.MaxEventSeconds) * time.Second,
	})
	p.eng.OnOpen = func(id string, at time.Time) {
		p.logf("event %s OPEN", id)
	}
	p.eng.OnClose = func(ev event.Event) {
		p.handleClose(ev, eventsDir)
	}

	if p.cfg.Recording.Enabled && p.cfg.Recording.RetentionHours > 0 {
		go retentionLoop(ctx, eventsDir, time.Duration(p.cfg.Recording.RetentionHours)*time.Hour)
	}

	frames := make(chan media.Frame, 4)
	go p.src.Run(ctx, frames)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	hb := time.NewTicker(time.Minute)
	defer hb.Stop()

	p.logf("running; classes=%v", rt.Classes)
	var nFrames, nDet int
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			p.eng.Tick(now)
		case <-hb.C:
			p.logf("heartbeat: %d frames, %d with detections", nFrames, nDet)
			nFrames, nDet = 0, 0
		case f := <-frames:
			nFrames++
			boxes, err := p.yolo.Detect(f.RGB)
			if err != nil {
				p.logf("detect: %v", err)
				continue
			}
			labels := uniqueLabels(boxes)
			hasDet := len(boxes) > 0
			if hasDet {
				nDet++
			}
			if p.deps.Metrics != nil {
				p.deps.Metrics.Frame(hasDet, labels)
			}
			p.eng.Update(f.TS, hasDet, labels)

			if p.deps.OnPreview != nil || hasDet {
				shot := p.annotate(f, boxes)
				p.mu.Lock()
				p.preview = shot
				p.mu.Unlock()
				if hasDet {
					p.dlog.add(f.TS, boxes)
					p.snaps.offer(f.TS, shot, totalScore(boxes))
				}
				if p.deps.OnPreview != nil {
					p.deps.OnPreview(shot)
				}
			}
		}
	}
}

func (p *Pipeline) handleClose(ev event.Event, eventsDir string) {
	rt := p.runtime()
	p.logf("event %s CLOSE (%s)", ev.ID, ev.End.Sub(ev.Start).Round(time.Second))

	rep := EventReport{
		ID:        ev.ID,
		Name:      p.cfg.Name,
		StartedAt: ev.Start,
		EndedAt:   ev.End,
		Classes:   ev.Classes,
		NotifyOn:  rt.NotifyOn,
	}

	from, to := ev.Start, ev.End
	if p.asm != nil && rt.RecordingOn {
		pre := time.Duration(rt.PreSeconds) * time.Second
		post := time.Duration(rt.PostSeconds) * time.Second
		time.Sleep(post + 2*time.Second) // let the post-roll reach the ring buffer
		from, to = ev.Start.Add(-pre), ev.End.Add(post)
	}

	recs := p.dlog.between(from, to)
	rep.ClassStats = classStats(recs)

	if p.asm != nil && rt.RecordingOn {
		overlays := buildOverlays(p.lb, recs)
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		clip, err := p.asm.Assemble(cctx, ev.ID, from, to, overlays)
		cancel()
		if err != nil {
			p.logf("event %s: assemble failed: %v", ev.ID, err)
		} else {
			rep.VideoPath = clip.VideoPath
			rep.SnapshotPath = clip.SnapshotPath
			if best := p.snaps.best(ev.Start, ev.End); best != nil {
				sp := filepath.Join(eventsDir, ev.ID+".jpg")
				if os.WriteFile(sp, best, 0o644) == nil {
					rep.SnapshotPath = sp
				}
			}
			if p.cfg.PublicURL != "" {
				rep.ClipURL = p.cfg.PublicURL + "/clips/" + filepath.Base(rep.VideoPath)
				if rep.SnapshotPath != "" {
					rep.SnapshotURL = p.cfg.PublicURL + "/clips/" + filepath.Base(rep.SnapshotPath)
				}
			}
		}
	} else if best := p.snaps.best(ev.Start, ev.End); best != nil {
		if err := os.MkdirAll(eventsDir, 0o755); err == nil {
			sp := filepath.Join(eventsDir, ev.ID+".jpg")
			if os.WriteFile(sp, best, 0o644) == nil {
				rep.SnapshotPath = sp
			}
		}
	}

	if p.deps.OnEvent != nil {
		p.deps.OnEvent(rep)
	}
}

func (p *Pipeline) probe(ctx context.Context) (media.Size, error) {
	backoff := 2 * time.Second
	for {
		sz, err := media.Probe(ctx, p.cfg.FFprobe, p.cfg.RTSPURL)
		if err == nil {
			return sz, nil
		}
		p.logf("probe failed: %v (retry in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return media.Size{}, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (p *Pipeline) logf(format string, args ...any) {
	if p.cfg.Name != "" {
		p.deps.Logf("["+p.cfg.Name+"] "+format, args...)
		return
	}
	p.deps.Logf(format, args...)
}

// annotate draws detection boxes (per-class color + confidence %) and returns JPEG.
func (p *Pipeline) annotate(f media.Frame, boxes []detect.Box) []byte {
	img := image.NewRGBA(image.Rect(0, 0, f.S, f.S))
	for i := 0; i < f.S*f.S; i++ {
		img.Pix[i*4+0] = f.RGB[i*3+0]
		img.Pix[i*4+1] = f.RGB[i*3+1]
		img.Pix[i*4+2] = f.RGB[i*3+2]
		img.Pix[i*4+3] = 255
	}
	detect.Annotate(img, boxes)
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75})
	return buf.Bytes()
}

func uniqueLabels(boxes []detect.Box) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range boxes {
		if !seen[b.Label] {
			seen[b.Label] = true
			out = append(out, b.Label)
		}
	}
	return out
}

// classStats aggregates per-label frame counts and peak confidence.
func classStats(recs []frameDet) []ClassStat {
	agg := map[string]*ClassStat{}
	for _, r := range recs {
		seen := map[string]bool{}
		for _, b := range r.boxes {
			s := agg[b.Label]
			if s == nil {
				s = &ClassStat{Label: b.Label}
				agg[b.Label] = s
			}
			if !seen[b.Label] {
				s.Count++
				seen[b.Label] = true
			}
			if b.Score > s.MaxScore {
				s.MaxScore = b.Score
			}
		}
	}
	out := make([]ClassStat, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

func retentionLoop(ctx context.Context, dir string, maxAge time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-maxAge)
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				info, err := e.Info()
				if err != nil {
					continue
				}
				if info.ModTime().Before(cutoff) {
					_ = os.Remove(filepath.Join(dir, e.Name()))
				}
			}
		}
	}
}
