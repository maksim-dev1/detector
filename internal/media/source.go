package media

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Frame is one decoded, letterboxed RGB frame ready for inference.
type Frame struct {
	RGB []byte // len = S*S*3, row-major packed RGB
	S   int
	TS  time.Time
}

// Segment is one recorded chunk of the stream (ring-buffer pre-roll).
type Segment struct {
	Path  string
	Start time.Time
	End   time.Time
}

// SourceConfig configures the single RTSP connection.
type SourceConfig struct {
	FFmpeg    string
	URL       string
	Transpose string // "" | "transpose=1" | ...
	Letterbox Letterbox
	FPS       int

	// Recording (optional). Empty RecordDir disables the segment output.
	RecordDir      string
	SegmentSeconds int
	RingSegments   int
}

// Source runs ONE ffmpeg process against the camera with two outputs:
// a raw RGB frame pipe for detection and a rolling .ts segment buffer for
// clip assembly. XM cameras allow only a few concurrent RTSP pulls, so a
// single connection matters. It supervises and restarts on failure.
type Source struct {
	cfg      SourceConfig
	listPath string

	mu      sync.Mutex
	started time.Time // ffmpeg launch wall-clock (segment time base)
}

func NewSource(cfg SourceConfig) *Source {
	s := &Source{cfg: cfg}
	if cfg.RecordDir != "" {
		s.listPath = filepath.Join(cfg.RecordDir, "segments.csv")
	}
	return s
}

func (s *Source) Run(ctx context.Context, out chan<- Frame) {
	if s.cfg.RecordDir != "" {
		if err := os.MkdirAll(s.cfg.RecordDir, 0o755); err != nil {
			log.Printf("source: mkdir: %v", err)
			return
		}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := s.runOnce(ctx, out)
		if ctx.Err() != nil {
			return
		}
		log.Printf("source: ffmpeg ended (%v), reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (s *Source) runOnce(ctx context.Context, out chan<- Frame) error {
	lb := s.cfg.Letterbox
	size := lb.S

	var vf []string
	if s.cfg.Transpose != "" {
		vf = append(vf, s.cfg.Transpose)
	}
	vf = append(vf,
		fmt.Sprintf("scale=%d:%d", lb.NewW, lb.NewH),
		fmt.Sprintf("pad=%d:%d:%d:%d:color=black", size, size, lb.PadX, lb.PadY),
		"format=rgb24",
	)

	args := []string{"-hide_banner", "-loglevel", "warning"}
	if isRTSP(s.cfg.URL) {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args,
		"-fflags", "nobuffer",
		"-i", s.cfg.URL,
		// output 1: detection frames
		"-map", "0:v:0", "-an",
		"-vf", strings.Join(vf, ","),
		"-r", strconv.Itoa(s.cfg.FPS),
		"-f", "rawvideo", "-pix_fmt", "rgb24", "pipe:1",
	)
	if s.cfg.RecordDir != "" {
		_ = os.Remove(s.listPath)
		pattern := filepath.Join(s.cfg.RecordDir, "seg_%05d.ts")
		args = append(args,
			// output 2: rolling segments, codec copy
			"-map", "0:v:0", "-an", "-c:v", "copy",
			"-f", "segment",
			"-segment_time", strconv.Itoa(s.cfg.SegmentSeconds),
			"-segment_format", "mpegts",
			"-segment_wrap", strconv.Itoa(s.cfg.RingSegments),
			"-segment_list", s.listPath,
			"-segment_list_type", "csv",
			"-segment_list_size", strconv.Itoa(s.cfg.RingSegments),
			"-reset_timestamps", "1",
			pattern,
		)
	}

	cmd := exec.Command(s.cfg.FFmpeg, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = &prefixWriter{prefix: "ffmpeg "}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pgid := cmd.Process.Pid

	s.mu.Lock()
	s.started = time.Now()
	s.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	readErr := make(chan error, 1)
	go func() { readErr <- s.readFrames(ctx, stdout, size, out) }()

	select {
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		return ctx.Err()
	case err := <-done:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return err
	case err := <-readErr:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		return err
	}
}

func (s *Source) readFrames(ctx context.Context, r io.Reader, size int, out chan<- Frame) error {
	frameLen := size * size * 3
	buf := make([]byte, frameLen)
	for ctx.Err() == nil {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		f := Frame{RGB: append([]byte(nil), buf...), S: size, TS: time.Now()}
		select {
		case out <- f:
		case <-ctx.Done():
			return ctx.Err()
		default:
			// detector behind; drop frame to stay live
		}
	}
	return ctx.Err()
}

// Between returns the segments overlapping [from, to], ordered by start time.
func (s *Source) Between(from, to time.Time) ([]Segment, error) {
	if s.listPath == "" {
		return nil, fmt.Errorf("recording disabled")
	}
	s.mu.Lock()
	base := s.started
	s.mu.Unlock()
	if base.IsZero() {
		return nil, fmt.Errorf("source not started yet")
	}

	f, err := os.Open(s.listPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var segs []Segment
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			continue
		}
		startSec, e1 := strconv.ParseFloat(parts[1], 64)
		endSec, e2 := strconv.ParseFloat(parts[2], 64)
		if e1 != nil || e2 != nil {
			continue
		}
		seg := Segment{
			Path:  filepath.Join(s.cfg.RecordDir, parts[0]),
			Start: base.Add(time.Duration(startSec * float64(time.Second))),
			End:   base.Add(time.Duration(endSec * float64(time.Second))),
		}
		if seg.End.Before(from) || seg.Start.After(to) {
			continue
		}
		if _, err := os.Stat(seg.Path); err != nil {
			continue // wrapped/overwritten
		}
		segs = append(segs, seg)
	}
	return segs, sc.Err()
}

type prefixWriter struct{ prefix string }

func (w *prefixWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.Contains(t, "reference picture") ||
			strings.Contains(t, "Missing reference") ||
			strings.Contains(t, "Guessed Channel Layout") ||
			strings.Contains(t, "Timestamps are unset") {
			continue
		}
		log.Print(w.prefix, t)
	}
	return len(p), nil
}
