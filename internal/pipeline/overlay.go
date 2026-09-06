package pipeline

import (
	"sync"
	"time"

	"github.com/maksim-dev1/detector/internal/detect"
	"github.com/maksim-dev1/detector/internal/media"
)

// buildOverlays maps recorded detection frames (model/letterbox space) into
// clip overlays (normalized, upright-source space) for burn-in.
func buildOverlays(lb media.Letterbox, recs []frameDet) []media.Overlay {
	out := make([]media.Overlay, 0, len(recs))
	for _, r := range recs {
		if len(r.boxes) == 0 {
			continue
		}
		o := media.Overlay{T: r.ts}
		for _, b := range r.boxes {
			nx1, ny1, nx2, ny2 := lb.ToSource(b.X1, b.Y1, b.X2, b.Y2)
			o.Boxes = append(o.Boxes, media.OverlayBox{
				NX1: nx1, NY1: ny1, NX2: nx2, NY2: ny2,
				Label: detect.Caption(b),
				Color: detect.ClassColor(b.Class),
			})
		}
		out = append(out, o)
	}
	return out
}

// frameDet is one detector frame's boxes at a wall-clock instant.
type frameDet struct {
	ts    time.Time
	boxes []detect.Box
}

// detLog keeps a rolling window of recent detection frames so a closed event can
// be replayed as burned-in overlays on its clip.
type detLog struct {
	mu   sync.Mutex
	recs []frameDet
	ttl  time.Duration
}

func newDetLog(ttl time.Duration) *detLog { return &detLog{ttl: ttl} }

func (l *detLog) add(ts time.Time, boxes []detect.Box) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, frameDet{ts: ts, boxes: boxes})
	cut := ts.Add(-l.ttl)
	i := 0
	for i < len(l.recs) && l.recs[i].ts.Before(cut) {
		i++
	}
	l.recs = l.recs[i:]
}

func (l *detLog) between(from, to time.Time) []frameDet {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []frameDet
	for _, r := range l.recs {
		if !r.ts.Before(from) && !r.ts.After(to) {
			out = append(out, r)
		}
	}
	return out
}

// snapItem is a captured annotated JPEG and its aggregate detection score.
type snapItem struct {
	ts    time.Time
	jpeg  []byte
	score float32
}

// snapRing keeps the last N annotated frames so an event can pick its best
// snapshot for the Telegram photo (boxes + confidence already drawn in Go).
type snapRing struct {
	mu    sync.Mutex
	items []snapItem
	max   int
}

func newSnapRing(n int) *snapRing { return &snapRing{max: n} }

func (r *snapRing) offer(ts time.Time, jpeg []byte, score float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, snapItem{ts: ts, jpeg: jpeg, score: score})
	if len(r.items) > r.max {
		r.items = r.items[len(r.items)-r.max:]
	}
}

// best returns the highest-scoring annotated frame within [from, to], or nil.
func (r *snapRing) best(from, to time.Time) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []byte
	var bs float32 = -1
	for _, it := range r.items {
		if it.ts.Before(from) || it.ts.After(to) {
			continue
		}
		if it.score > bs {
			bs, out = it.score, it.jpeg
		}
	}
	return out
}

func totalScore(boxes []detect.Box) float32 {
	var s float32
	for _, b := range boxes {
		s += b.Score
	}
	return s
}
