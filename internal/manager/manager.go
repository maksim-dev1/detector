// Package manager owns the set of running pipelines and keeps it in sync with
// the stream rows in the store: starting, stopping, restarting and live-
// reconfiguring pipelines as rows change.
package manager

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/maksim-dev1/detector/internal/pipeline"
	"github.com/maksim-dev1/detector/internal/store"
)

// Base holds the process-wide settings every pipeline shares.
type Base struct {
	FFmpeg, FFprobe string
	PublicURL       string

	YoloURL       string
	InputSize, FPS int
	IOUThreshold  float32

	Event pipeline.EventConfig

	RecordingsRoot   string
	RecordingEnabled bool
	SegmentSeconds   int
	RingSegments     int
	RetentionHours   int

	MaxStreamsPerUser int
	MaxStreamsTotal   int
}

// Deps are the manager-level side channels.
type Deps struct {
	OnEvent   func(st store.Stream, rep pipeline.EventReport)
	OnPreview func(streamID int64, jpeg []byte)
	Metrics   func(streamID int64) pipeline.Metrics // optional per-stream metrics sink
	Logf      func(string, ...any)
}

type Manager struct {
	base  Base
	st    *store.Store
	deps  Deps
	rootc context.Context

	mu   sync.Mutex
	live map[int64]*handle
}

type handle struct {
	stream store.Stream
	pipe   *pipeline.Pipeline
	cancel context.CancelFunc
	done   chan struct{}
}

func New(ctx context.Context, st *store.Store, base Base, deps Deps) *Manager {
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	return &Manager{base: base, st: st, deps: deps, rootc: ctx, live: map[int64]*handle{}}
}

// Run does an initial Sync then re-syncs on a ticker until ctx is done.
func (m *Manager) Run() {
	m.Sync()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.rootc.Done():
			m.stopAll()
			return
		case <-t.C:
			m.Sync()
		}
	}
}

// Sync reconciles running pipelines with the store.
func (m *Manager) Sync() {
	streams, err := m.st.ListStreams()
	if err != nil {
		m.deps.Logf("manager: list streams: %v", err)
		return
	}
	want := map[int64]store.Stream{}
	for _, s := range streams {
		if s.Enabled {
			want[s.ID] = s
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// stop pipelines that are gone, disabled, or need a hard restart
	for id, h := range m.live {
		w, ok := want[id]
		if !ok {
			m.stopLocked(id)
			continue
		}
		if needsRestart(h.stream, w) {
			m.deps.Logf("manager: stream %d changed (url/rotate), restarting", id)
			m.stopLocked(id)
			continue
		}
		if !reflect.DeepEqual(h.stream, w) {
			h.stream = w
			h.pipe.Reconfigure(m.runtime(w))
			m.deps.Logf("manager: stream %d reconfigured", id)
		}
	}

	// start pipelines that should be running but aren't
	for id, w := range want {
		if _, ok := m.live[id]; ok {
			continue
		}
		m.startLocked(w)
	}
}

func needsRestart(a, b store.Stream) bool {
	return a.URL != b.URL || a.Rotate != b.Rotate
}

func (m *Manager) startLocked(s store.Stream) {
	ctx, cancel := context.WithCancel(m.rootc)
	var metrics pipeline.Metrics
	if m.deps.Metrics != nil {
		metrics = m.deps.Metrics(s.ID)
	}
	p := pipeline.New(m.config(s), m.runtime(s), pipeline.Deps{
		OnPreview: func(j []byte) {
			if m.deps.OnPreview != nil {
				m.deps.OnPreview(s.ID, j)
			}
		},
		OnEvent: func(rep pipeline.EventReport) {
			if m.deps.OnEvent != nil {
				m.deps.OnEvent(s, rep)
			}
		},
		Metrics: metrics,
		Logf:    m.deps.Logf,
	})
	h := &handle{stream: s, pipe: p, cancel: cancel, done: make(chan struct{})}
	m.live[s.ID] = h
	m.deps.Logf("manager: starting stream %d (%s)", s.ID, s.Name)
	go func() {
		err := p.Run(ctx)
		close(h.done)
		m.mu.Lock()
		if m.live[s.ID] == h {
			delete(m.live, s.ID)
		}
		m.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			m.deps.Logf("manager: stream %d exited: %v (will retry on next sync)", s.ID, err)
		}
	}()
}

func (m *Manager) stopLocked(id int64) {
	h, ok := m.live[id]
	if !ok {
		return
	}
	delete(m.live, id)
	m.deps.Logf("manager: stopping stream %d", id)
	h.cancel()
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	handles := make([]*handle, 0, len(m.live))
	for id, h := range m.live {
		handles = append(handles, h)
		delete(m.live, id)
	}
	m.mu.Unlock()
	for _, h := range handles {
		h.cancel()
	}
	for _, h := range handles {
		<-h.done
	}
}

// Snapshot returns the latest annotated frame for a stream, or nil.
func (m *Manager) Snapshot(id int64) []byte {
	m.mu.Lock()
	h, ok := m.live[id]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return h.pipe.Snapshot()
}

// Running reports whether a pipeline is currently alive for the stream.
func (m *Manager) Running(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.live[id]
	return ok
}

// CheckQuota returns an error if owner may not add another stream.
func (m *Manager) CheckQuota(owner int64) error {
	total, err := len0(m.st.ListStreams())
	if err != nil {
		return err
	}
	if m.base.MaxStreamsTotal > 0 && total >= m.base.MaxStreamsTotal {
		return fmt.Errorf("достигнут общий лимит потоков (%d)", m.base.MaxStreamsTotal)
	}
	own, err := m.st.CountStreamsByOwner(owner)
	if err != nil {
		return err
	}
	if m.base.MaxStreamsPerUser > 0 && own >= m.base.MaxStreamsPerUser {
		return fmt.Errorf("достигнут твой лимит потоков (%d)", m.base.MaxStreamsPerUser)
	}
	return nil
}

func len0(s []store.Stream, err error) (int, error) { return len(s), err }

func (m *Manager) config(s store.Stream) pipeline.Config {
	publicURL := ""
	if m.base.PublicURL != "" {
		publicURL = fmt.Sprintf("%s/s/%d", m.base.PublicURL, s.ID)
	}
	return pipeline.Config{
		Name:      s.Name,
		RTSPURL:   s.URL,
		Rotate:    s.Rotate,
		FFmpeg:    m.base.FFmpeg,
		FFprobe:   m.base.FFprobe,
		PublicURL: publicURL,
		Detect: pipeline.DetectConfig{
			YoloURL:       m.base.YoloURL,
			InputSize:     m.base.InputSize,
			FPS:           m.base.FPS,
			ConfThreshold: s.Conf,
			IOUThreshold:  m.base.IOUThreshold,
			Classes:       s.Classes,
		},
		Event: m.base.Event,
		Recording: pipeline.RecordingConfig{
			Enabled:        m.base.RecordingEnabled,
			Dir:            filepath.Join(m.base.RecordingsRoot, fmt.Sprint(s.ID)),
			SegmentSeconds: m.base.SegmentSeconds,
			RingSegments:   m.base.RingSegments,
			RetentionHours: m.base.RetentionHours,
		},
	}
}

func (m *Manager) runtime(s store.Stream) pipeline.Runtime {
	return pipeline.Runtime{
		ConfThreshold:   s.Conf,
		Classes:         s.Classes,
		TriggerFrames:   m.base.Event.TriggerFrames,
		CooldownSeconds: m.base.Event.CooldownSeconds,
		PreSeconds:      m.base.Event.PreSeconds,
		PostSeconds:     m.base.Event.PostSeconds,
		RecordingOn:     m.base.RecordingEnabled,
		NotifyOn:        true,
	}
}
