// Package config loads the detector-service YAML config (and env overrides).
// Per-stream settings live in the store, not here — this is process-wide setup.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/maksim-dev1/detector/internal/media"
)

type Config struct {
	Bot       Bot       `yaml:"bot"`
	Store     Store     `yaml:"store"`
	FFmpeg    FFmpeg    `yaml:"ffmpeg"`
	Detect    Detect    `yaml:"detect"`
	Event     Event     `yaml:"event"`
	Recording Recording `yaml:"recording"`
	Limits    Limits    `yaml:"limits"`
	Server    Server    `yaml:"server"`
	Webhook   Webhook   `yaml:"webhook"`
	Seed      *Seed     `yaml:"seed"`
}

type Bot struct {
	Token       string `yaml:"token"`
	AdminChatID int64  `yaml:"admin_chat_id"`
}

type Store struct {
	Path string `yaml:"path"`
}

type FFmpeg struct {
	Bin   string `yaml:"bin"`
	Probe string `yaml:"probe"`
}

type Detect struct {
	YoloURL      string  `yaml:"yolo_url"`
	InputSize    int     `yaml:"input_size"`
	FPS          int     `yaml:"fps"`
	IOUThreshold float32 `yaml:"iou_threshold"`
	DefaultConf  float32 `yaml:"default_conf"`
}

type Event struct {
	TriggerFrames   int `yaml:"trigger_frames"`
	CooldownSeconds int `yaml:"cooldown_seconds"`
	MinEventSeconds int `yaml:"min_event_seconds"`
	PreSeconds      int `yaml:"pre_seconds"`
	PostSeconds     int `yaml:"post_seconds"`
	MaxEventSeconds int `yaml:"max_event_seconds"`
}

type Recording struct {
	Enabled        bool   `yaml:"enabled"`
	Dir            string `yaml:"dir"`
	SegmentSeconds int    `yaml:"segment_seconds"`
	RingSegments   int    `yaml:"ring_segments"`
	RetentionHours int    `yaml:"retention_hours"`
}

type Limits struct {
	MaxStreamsPerUser int `yaml:"max_streams_per_user"`
	MaxStreamsTotal   int `yaml:"max_streams_total"`
}

type Server struct {
	Enabled   bool   `yaml:"enabled"`
	Addr      string `yaml:"addr"`
	PublicURL string `yaml:"public_url"`
}

type Webhook struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
}

// Seed optionally auto-adds one stream owned by the admin on first run.
type Seed struct {
	Name    string `yaml:"name"`
	RTSPURL string `yaml:"rtsp_url"`
	Rotate  string `yaml:"rotate"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	c.applyEnv()
	c.defaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("BOT_TOKEN"); v != "" {
		c.Bot.Token = v
	}
	if v := os.Getenv("ADMIN_CHAT_ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Bot.AdminChatID = id
		}
	}
	if v := os.Getenv("YOLO_URL"); v != "" {
		c.Detect.YoloURL = v
	}
	if v := os.Getenv("WEBHOOK_URL"); v != "" {
		c.Webhook.URL = v
		c.Webhook.Enabled = true
	}
	if v := os.Getenv("SEED_RTSP_URL"); v != "" && c.Seed != nil {
		c.Seed.RTSPURL = v
	}
}

func (c *Config) defaults() {
	set := func(p *string, v string) {
		if strings.TrimSpace(*p) == "" {
			*p = v
		}
	}
	set(&c.Store.Path, "detector.db")
	set(&c.FFmpeg.Bin, "ffmpeg")
	set(&c.FFmpeg.Probe, "ffprobe")
	set(&c.Recording.Dir, "recordings")
	set(&c.Server.Addr, ":8000")

	d := &c.Detect
	if d.InputSize == 0 {
		d.InputSize = 640
	}
	if d.FPS == 0 {
		d.FPS = 6
	}
	if d.IOUThreshold == 0 {
		d.IOUThreshold = 0.45
	}
	if d.DefaultConf == 0 {
		d.DefaultConf = 0.35
	}

	e := &c.Event
	if e.TriggerFrames == 0 {
		e.TriggerFrames = 3
	}
	if e.CooldownSeconds == 0 {
		e.CooldownSeconds = 8
	}
	if e.MinEventSeconds == 0 {
		e.MinEventSeconds = 2
	}
	if e.PreSeconds == 0 {
		e.PreSeconds = 6
	}
	if e.PostSeconds == 0 {
		e.PostSeconds = 6
	}
	if e.MaxEventSeconds == 0 {
		e.MaxEventSeconds = 120
	}

	r := &c.Recording
	if r.SegmentSeconds == 0 {
		r.SegmentSeconds = 2
	}
	if r.RingSegments == 0 {
		r.RingSegments = 150
	}
	if r.RetentionHours == 0 {
		r.RetentionHours = 72
	}

	l := &c.Limits
	if l.MaxStreamsPerUser == 0 {
		l.MaxStreamsPerUser = 5
	}
	if l.MaxStreamsTotal == 0 {
		l.MaxStreamsTotal = 20
	}

	if c.Seed != nil && strings.TrimSpace(c.Seed.Rotate) == "" {
		c.Seed.Rotate = "none"
	}
}

func (c *Config) validate() error {
	if c.Bot.Token == "" {
		return fmt.Errorf("bot.token is required (or $BOT_TOKEN)")
	}
	if c.Bot.AdminChatID == 0 {
		return fmt.Errorf("bot.admin_chat_id is required (or $ADMIN_CHAT_ID)")
	}
	if c.Detect.YoloURL == "" {
		return fmt.Errorf("detect.yolo_url is required (or $YOLO_URL)")
	}
	if c.Detect.InputSize%32 != 0 {
		return fmt.Errorf("detect.input_size must be a multiple of 32")
	}
	if c.Seed != nil {
		if c.Seed.RTSPURL == "" {
			return fmt.Errorf("seed.rtsp_url is required when seed is set")
		}
		if !ValidRotate(c.Seed.Rotate) {
			return fmt.Errorf("seed.rotate must be none|90cw|90ccw|180")
		}
	}
	return nil
}

// ValidRotate reports whether s is an accepted rotation mode.
func ValidRotate(s string) bool {
	switch s {
	case "none", "90cw", "90ccw", "180":
		return true
	}
	return false
}

// TransposeFilter is re-exported for callers that only import config.
func TransposeFilter(rotate string) string { return media.TransposeFilter(rotate) }
