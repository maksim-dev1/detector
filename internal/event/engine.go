package event

import (
	"sync"
	"time"
)

type state int

const (
	idle state = iota
	active
)

// Config controls how detections are debounced into discrete events.
type Config struct {
	TriggerFrames int
	Cooldown      time.Duration
	MinDuration   time.Duration
	MaxDuration   time.Duration
}

// Event is a closed detection window.
type Event struct {
	ID      string
	Start   time.Time
	End     time.Time
	Classes []string
}

// Engine turns a stream of per-frame booleans into open/close callbacks.
// All methods are safe for concurrent use.
type Engine struct {
	cfg     Config
	mu      sync.Mutex
	st      state
	streak  int
	start   time.Time
	lastPos time.Time
	classes map[string]bool
	curID   string

	OnOpen  func(id string, at time.Time)
	OnClose func(ev Event)
}

func New(cfg Config) *Engine {
	return &Engine{cfg: cfg, classes: map[string]bool{}}
}

// Update feeds one frame's result. labels is the set of detected wanted classes.
func (e *Engine) Update(now time.Time, positive bool, labels []string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if positive {
		e.streak++
		e.lastPos = now
		for _, l := range labels {
			e.classes[l] = true
		}
		if e.st == idle && e.streak >= e.cfg.TriggerFrames {
			e.st = active
			e.start = now
			e.curID = now.Format("20060102-150405")
			if e.OnOpen != nil {
				go e.OnOpen(e.curID, now)
			}
		}
	} else {
		e.streak = 0
	}
	e.maybeClose(now)
}

// SetParams updates the debounce parameters at runtime.
func (e *Engine) SetParams(cfg Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

// Tick lets the engine close a stale event even when frames stop arriving.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.maybeClose(now)
}

func (e *Engine) maybeClose(now time.Time) {
	if e.st != active {
		return
	}
	cooled := now.Sub(e.lastPos) >= e.cfg.Cooldown
	tooLong := e.cfg.MaxDuration > 0 && now.Sub(e.start) >= e.cfg.MaxDuration
	if !cooled && !tooLong {
		return
	}

	end := e.lastPos
	if tooLong {
		end = now
	}
	ev := Event{
		ID:      e.curID,
		Start:   e.start,
		End:     end,
		Classes: keys(e.classes),
	}

	e.st = idle
	e.streak = 0
	e.classes = map[string]bool{}
	e.curID = ""

	if end.Sub(ev.Start) < e.cfg.MinDuration {
		return // too short, drop
	}
	if e.OnClose != nil {
		go e.OnClose(ev)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
