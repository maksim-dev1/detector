// Command detector is the Telegram detection service: it runs a bot that lets
// approved users add camera streams, runs a YOLO pipeline per stream, and
// delivers finished detection events to each stream's owner.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maksim-dev1/detector/internal/bot"
	"github.com/maksim-dev1/detector/internal/config"
	"github.com/maksim-dev1/detector/internal/manager"
	"github.com/maksim-dev1/detector/internal/notify"
	"github.com/maksim-dev1/detector/internal/pipeline"
	"github.com/maksim-dev1/detector/internal/store"
	"github.com/maksim-dev1/detector/internal/tg"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()
	if err := st.EnsureAdmin(cfg.Bot.AdminChatID); err != nil {
		log.Fatalf("store: ensure admin: %v", err)
	}
	seedStream(st, cfg)

	tgc := tg.New(cfg.Bot.Token)
	nf := notify.New(tgc)
	if cfg.Webhook.Enabled {
		nf = nf.WithWebhook(cfg.Webhook.URL)
	}

	publicURL := ""
	if cfg.Server.Enabled {
		publicURL = strings.TrimRight(cfg.Server.PublicURL, "/")
	}

	b := bot.New(tgc, st, nf, bot.Config{
		AdminChatID: cfg.Bot.AdminChatID,
		FFprobe:     cfg.FFmpeg.Probe,
		PublicURL:   publicURL,
		DefaultConf: cfg.Detect.DefaultConf,
	}, log.Printf)

	previews := &previewStore{}

	mgr := manager.New(ctx, st, manager.Base{
		FFmpeg:       cfg.FFmpeg.Bin,
		FFprobe:      cfg.FFmpeg.Probe,
		PublicURL:    publicURL,
		YoloURL:      cfg.Detect.YoloURL,
		InputSize:    cfg.Detect.InputSize,
		FPS:          cfg.Detect.FPS,
		IOUThreshold: cfg.Detect.IOUThreshold,
		Event: pipeline.EventConfig{
			TriggerFrames:   cfg.Event.TriggerFrames,
			CooldownSeconds: cfg.Event.CooldownSeconds,
			MinEventSeconds: cfg.Event.MinEventSeconds,
			PreSeconds:      cfg.Event.PreSeconds,
			PostSeconds:     cfg.Event.PostSeconds,
			MaxEventSeconds: cfg.Event.MaxEventSeconds,
		},
		RecordingsRoot:    cfg.Recording.Dir,
		RecordingEnabled:  cfg.Recording.Enabled,
		SegmentSeconds:    cfg.Recording.SegmentSeconds,
		RingSegments:      cfg.Recording.RingSegments,
		RetentionHours:    cfg.Recording.RetentionHours,
		MaxStreamsPerUser: cfg.Limits.MaxStreamsPerUser,
		MaxStreamsTotal:   cfg.Limits.MaxStreamsTotal,
	}, manager.Deps{
		OnEvent:   b.OnEvent,
		OnPreview: previews.set,
		Logf:      log.Printf,
	})
	b.SetManager(mgr)

	if cfg.Server.Enabled {
		go serveHTTP(ctx, cfg.Server.Addr, cfg.Recording.Dir, previews)
		log.Printf("http listening on %s", cfg.Server.Addr)
	}

	go mgr.Run()
	log.Printf("service up; admin=%d", cfg.Bot.AdminChatID)

	if err := b.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("bot: %v", err)
	}
	log.Print("shutting down")
	time.Sleep(500 * time.Millisecond)
}

// seedStream inserts the configured seed camera once (owned by the admin) if no
// stream with that URL exists yet.
func seedStream(st *store.Store, cfg *config.Config) {
	if cfg.Seed == nil {
		return
	}
	existing, err := st.ListStreams()
	if err != nil {
		log.Printf("seed: %v", err)
		return
	}
	for _, s := range existing {
		if s.URL == cfg.Seed.RTSPURL {
			return
		}
	}
	s, err := st.AddStream(store.Stream{
		Owner: cfg.Bot.AdminChatID, Name: cfg.Seed.Name, URL: cfg.Seed.RTSPURL,
		Rotate: cfg.Seed.Rotate, Conf: cfg.Detect.DefaultConf, Enabled: true,
	})
	if err != nil {
		log.Printf("seed: %v", err)
		return
	}
	log.Printf("seed: added stream #%d %q", s.ID, s.Name)
}

// previewStore keeps the latest annotated JPEG per stream for the HTTP preview.
type previewStore struct{ m sync.Map }

func (p *previewStore) set(streamID int64, jpeg []byte) { p.m.Store(streamID, jpeg) }
func (p *previewStore) get(streamID int64) []byte {
	if v, ok := p.m.Load(streamID); ok {
		return v.([]byte)
	}
	return nil
}

func eventsDir(root string, streamID int64) string {
	return filepath.Join(root, fmt.Sprint(streamID), "events")
}
