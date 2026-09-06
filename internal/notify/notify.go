// Package notify turns a finished detection event into one rich Telegram
// message (video + thumbnail + caption + buttons, with photo/text fallbacks)
// and, optionally, a JSON webhook POST.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/maksim-dev1/detector/internal/detect"
	"github.com/maksim-dev1/detector/internal/tg"
)

// ClassLine is a per-label summary shown in the caption.
type ClassLine struct {
	Label    string
	Count    int
	MaxScore float32
}

// Event is everything needed to render one notification.
type Event struct {
	ChatID     int64
	StreamName string
	EventID    string
	StartedAt  time.Time
	EndedAt    time.Time
	Classes    []ClassLine

	VideoPath    string         // burned-in clip; may be ""
	SnapshotPath string         // annotated still; may be ""
	ClipURL      string         // public clip URL, for the webhook payload only
	Markup       map[string]any // optional prebuilt inline keyboard
}

// Notifier sends events to Telegram and (optionally) a webhook.
type Notifier struct {
	tg         *tg.Client
	hc         *http.Client
	webhookURL string
}

func New(tgClient *tg.Client) *Notifier {
	return &Notifier{tg: tgClient, hc: &http.Client{Timeout: 30 * time.Second}}
}

func (n *Notifier) WithWebhook(url string) *Notifier { n.webhookURL = url; return n }

// SendEvent posts the webhook (if configured) and one Telegram message.
func (n *Notifier) SendEvent(ctx context.Context, ev Event) error {
	if n.webhookURL != "" {
		n.postWebhook(ctx, ev)
	}
	if n.tg == nil || ev.ChatID == 0 {
		return nil
	}

	caption := buildCaption(ev)
	markup := ev.Markup

	video, _ := os.ReadFile(ev.VideoPath)
	snap, _ := os.ReadFile(ev.SnapshotPath)

	switch {
	case len(video) > 0:
		var thumb *tg.FileField
		if small := shrink(snap, 320); len(small) > 0 {
			thumb = &tg.FileField{Field: "thumb", Filename: "thumb.jpg", Data: small}
		}
		_, err := n.tg.SendVideo(ctx, ev.ChatID,
			tg.FileField{Field: "video", Filename: filepath.Base(ev.VideoPath), Data: video},
			thumb, caption, markup)
		return err
	case len(snap) > 0:
		_, err := n.tg.SendPhoto(ctx, ev.ChatID,
			tg.FileField{Field: "photo", Filename: "snap.jpg", Data: snap}, caption, markup)
		return err
	default:
		_, err := n.tg.SendMessage(ctx, ev.ChatID, caption, markup)
		return err
	}
}

func buildCaption(ev Event) string {
	var b strings.Builder

	title := "Движение"
	if len(ev.Classes) > 0 {
		top := ev.Classes[0]
		title = fmt.Sprintf("%s · %.0f%%", detect.RussianName(top.Label), top.MaxScore*100)
	}
	fmt.Fprintf(&b, "🎯 <b>%s</b>\n", html.EscapeString(title))

	if ev.StreamName != "" {
		fmt.Fprintf(&b, "📹 %s", html.EscapeString(ev.StreamName))
	}
	dur := ev.EndedAt.Sub(ev.StartedAt).Round(time.Second)
	if dur > 0 {
		if ev.StreamName != "" {
			b.WriteString("  ·  ")
		}
		fmt.Fprintf(&b, "🔴 %s", humanDur(dur))
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "🕐 %s → %s\n",
		ev.StartedAt.Format("02.01 15:04:05"), ev.EndedAt.Format("15:04:05"))

	if len(ev.Classes) > 0 {
		parts := make([]string, 0, len(ev.Classes))
		for _, c := range ev.Classes {
			parts = append(parts, fmt.Sprintf("%s ×%d", html.EscapeString(detect.RussianName(c.Label)), c.Count))
		}
		fmt.Fprintf(&b, "🏷 %s", strings.Join(parts, " · "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%dс", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dм", m)
	}
	return fmt.Sprintf("%dм %02dс", m, s)
}

// shrink decodes a JPEG and re-encodes it fitted into a max×max box.
func shrink(jpg []byte, max int) []byte {
	if len(jpg) == 0 {
		return nil
	}
	src, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return jpg
	}
	scale := float64(max) / float64(w)
	if h > w {
		scale = float64(max) / float64(h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, int(float64(w)*scale), int(float64(h)*scale)))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	var out bytes.Buffer
	if jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80}) != nil {
		return nil
	}
	return out.Bytes()
}

// --- webhook ---

func (n *Notifier) postWebhook(ctx context.Context, ev Event) {
	payload := map[string]any{
		"event_id":   ev.EventID,
		"stream":     ev.StreamName,
		"started_at": ev.StartedAt,
		"ended_at":   ev.EndedAt,
		"classes":    ev.Classes,
		"clip_url":   ev.ClipURL,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.hc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
}
