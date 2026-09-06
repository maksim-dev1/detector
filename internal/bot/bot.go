// Package bot is the Telegram control-plane. The UI is a persistent reply
// keyboard (buttons replace the phone keyboard); tapping a button sends its
// label, which the bot routes against the chat's current screen. Free text is
// only consumed when a screen explicitly asked for it. Event notifications and
// the admin approval prompt keep inline buttons (they act on one specific item).
package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maksim-dev1/detector/internal/detect"
	"github.com/maksim-dev1/detector/internal/manager"
	"github.com/maksim-dev1/detector/internal/media"
	"github.com/maksim-dev1/detector/internal/notify"
	"github.com/maksim-dev1/detector/internal/pipeline"
	"github.com/maksim-dev1/detector/internal/store"
	"github.com/maksim-dev1/detector/internal/tg"
)

type Config struct {
	AdminChatID  int64
	FFprobe      string
	PublicURL    string
	DefaultConf  float32
	ProbeTimeout time.Duration
}

type Bot struct {
	tg   *tg.Client
	st   *store.Store
	mgr  *manager.Manager
	nf   *notify.Notifier
	cfg  Config
	logf func(string, ...any)

	mu  sync.Mutex
	nav map[int64]*navState
}

// screens
const (
	scrMain    = "main"
	scrCameras = "cameras"
	scrCamera  = "camera"
	scrClasses = "classes"
	scrAllCls  = "allclasses"
	scrConf    = "conf"
	scrRotate  = "rotate"
	scrMute    = "mute"
	scrDelete  = "delete"
	scrUsers   = "users"
)

type navState struct {
	screen  string
	stream  int64
	page    int
	prompt  string // "" | "add_url" | "add_name" | "conf" | "classes" | "rename"
	data    string
	touched time.Time
}

const clsPerPage = 8

// clsDisplay maps a shown class label back to its COCO name.
var clsDisplay = func() map[string]string {
	m := make(map[string]string, len(detect.COCONames)*2)
	for _, en := range detect.COCONames {
		ru := detect.RussianName(en)
		m[ru] = en
		m[strings.ToLower(ru)] = en
	}
	return m
}()

// classFromLabel resolves a button label or typed name (RU or EN) to a COCO
// class name, or "" if unknown.
func classFromLabel(label string) string {
	label = strings.TrimSpace(strings.TrimPrefix(label, "✅ "))
	if en, ok := clsDisplay[label]; ok {
		return en
	}
	if en, ok := clsDisplay[strings.ToLower(label)]; ok {
		return en
	}
	if detect.ClassIndex(strings.ToLower(label)) >= 0 {
		return strings.ToLower(label)
	}
	return ""
}

// button labels
const (
	bBack    = "⬅️ Назад"
	bCameras = "📹 Камеры"
	bAdd     = "➕ Добавить"
	bHelp    = "❓ Помощь"
	bUsers   = "👥 Пользователи"
	bStreams = "📡 Все потоки"
	bManual  = "✏️ Ввести вручную"
	bAllCls  = "📋 Все классы"
	bCatalog = "ℹ️ Описание"
	bPrev    = "◀"
	bNext    = "▶"
	bSnap    = "📸 Кадр"
	bClasses = "🏷 Классы"
	bConf    = "🎚 Порог"
	bRotate  = "🔄 Поворот"
	bMute    = "🔇 Пауза"
	bName    = "✏️ Имя"
	bDelete  = "🗑 Удалить"
	bEnable  = "▶️ Включить"
	bDisable = "⏸ Выключить"
	bDelYes  = "🗑 Да, удалить"
	bMuteOff = "🔔 Снять паузу"
	bRot0    = "Без поворота"
	bRot180  = "180°"
	bRotCW   = "90° ↻"
	bRotCCW  = "90° ↺"
)

var classPalette = []string{"person", "car", "dog", "cat", "bicycle", "motorcycle", "truck", "bus"}
var confPresets = []int{20, 30, 40, 50, 60}

func New(tgc *tg.Client, st *store.Store, nf *notify.Notifier, cfg Config, logf func(string, ...any)) *Bot {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = 20 * time.Second
	}
	return &Bot{tg: tgc, st: st, nf: nf, cfg: cfg, logf: logf, nav: map[int64]*navState{}}
}

func (b *Bot) SetManager(m *manager.Manager) { b.mgr = m }

func (b *Bot) Run(ctx context.Context) error {
	me, err := b.tg.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("getMe: %w", err)
	}
	_ = b.tg.Call(ctx, "setMyCommands", map[string]any{
		"commands": []map[string]string{{"command": "start", "description": "Меню"}},
	}, nil)
	b.logf("bot: @%s online", me.Username)

	var offset int64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ups, err := b.tg.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.logf("bot: getUpdates: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			b.dispatch(ctx, u)
		}
	}
}

func (b *Bot) dispatch(ctx context.Context, u tg.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.logf("bot: panic on update %d: %v", u.UpdateID, r)
		}
	}()
	switch {
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.From != nil:
		b.onMessage(ctx, u.Message)
	}
}

// --- nav state ---

func (b *Bot) navOf(chatID int64) *navState {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.nav[chatID]
	if n == nil || time.Since(n.touched) > 30*time.Minute {
		n = &navState{screen: scrMain}
		b.nav[chatID] = n
	}
	n.touched = time.Now()
	return n
}

func (b *Bot) setNav(chatID int64, screen string, stream int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nav[chatID] = &navState{screen: screen, stream: stream, touched: time.Now()}
}

func (b *Bot) setPrompt(chatID int64, kind, data string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := b.nav[chatID]; n != nil {
		n.prompt, n.data = kind, data
		n.touched = time.Now()
	}
}

// --- access ---

func (b *Bot) gate(ctx context.Context, chatID int64) (store.User, bool) {
	if chatID == b.cfg.AdminChatID {
		_ = b.st.EnsureAdmin(chatID)
		u, _, _ := b.st.GetUser(chatID)
		return u, true
	}
	u, ok, err := b.st.GetUser(chatID)
	if err != nil {
		b.say(ctx, chatID, "внутренняя ошибка", nil)
		return store.User{}, false
	}
	if !ok || u.Status == store.StatusPending {
		b.say(ctx, chatID, "Заявка на рассмотрении. Дождись одобрения.", tg.RemoveKeyboard())
		return store.User{}, false
	}
	if u.Status == store.StatusBlocked {
		b.say(ctx, chatID, "Доступ закрыт.", tg.RemoveKeyboard())
		return store.User{}, false
	}
	return u, true
}

// --- messages ---

func (b *Bot) onMessage(ctx context.Context, m *tg.Message) {
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)
	uname := ""
	if m.From != nil {
		uname = m.From.Username
	}

	if strings.HasPrefix(text, "/start") || strings.HasPrefix(text, "/menu") {
		if chatID != b.cfg.AdminChatID {
			b.registerIfNew(ctx, chatID, uname)
		}
		u, ok := b.gate(ctx, chatID)
		if !ok {
			return
		}
		b.setNav(chatID, scrMain, 0)
		b.render(ctx, u)
		return
	}

	u, ok := b.gate(ctx, chatID)
	if !ok {
		return
	}
	n := b.navOf(chatID)

	// a screen asked for free text — consume it unless the user tapped Back
	if n.prompt != "" && text != bBack {
		b.handlePrompt(ctx, u, n, text)
		return
	}
	n.prompt, n.data = "", ""

	b.route(ctx, u, n, text)
}

func (b *Bot) registerIfNew(ctx context.Context, chatID int64, uname string) {
	usr, _ := b.st.UpsertUser(chatID, uname)
	if usr.Status != store.StatusPending {
		return
	}
	who := "id " + id(chatID)
	if uname != "" {
		who = "@" + uname + " · " + who
	}
	_, _ = b.tg.SendMessage(ctx, b.cfg.AdminChatID, "Новая заявка: "+who, tg.Keyboard(
		[]tg.InlineButton{
			{Text: "✅ Одобрить", Data: "approve:" + id(chatID)},
			{Text: "❌ Отклонить", Data: "deny:" + id(chatID)},
		}))
}

// route matches a button label against the current screen.
func (b *Bot) route(ctx context.Context, u store.User, n *navState, text string) {
	switch n.screen {
	case scrMain:
		switch text {
		case bCameras:
			b.goCameras(ctx, u)
		case bAdd:
			b.setPrompt(u.ChatID, "add_url", "")
			b.say(ctx, u.ChatID, "Пришли RTSP-ссылку камеры.\nНапр.: rtsp://user:pass@192.168.1.10:554/stream", kbCancel())
		case bHelp:
			b.say(ctx, u.ChatID, helpText(u.IsAdmin), nil)
		case bUsers:
			if u.IsAdmin {
				b.goUsers(ctx, u)
			}
		case bStreams:
			if u.IsAdmin {
				b.say(ctx, u.ChatID, b.allStreamsText(), nil)
			}
		default:
			b.render(ctx, u)
		}

	case scrCameras:
		if text == bBack {
			b.setNav(u.ChatID, scrMain, 0)
			b.render(ctx, u)
			return
		}
		if text == bAdd {
			b.setPrompt(u.ChatID, "add_url", "")
			b.say(ctx, u.ChatID, "Пришли RTSP-ссылку камеры.", kbCancel())
			return
		}
		if sid := parseHashID(text); sid != 0 {
			if s, ok, _ := b.st.GetStream(sid); ok && b.owns(u, s) {
				b.setNav(u.ChatID, scrCamera, sid)
				b.render(ctx, u)
				return
			}
		}
		b.render(ctx, u)

	case scrCamera:
		b.routeCamera(ctx, u, n, text)

	case scrClasses:
		s, ok := b.curStream(u, n)
		if !ok {
			b.goCameras(ctx, u)
			return
		}
		switch text {
		case bBack:
			b.setNav(u.ChatID, scrCamera, s.ID)
			b.render(ctx, u)
		case bAllCls:
			b.sayHTML(ctx, u.ChatID, catalogText())
			b.setNav(u.ChatID, scrAllCls, s.ID)
			b.render(ctx, u)
		case bCatalog:
			b.sayHTML(ctx, u.ChatID, catalogText())
			b.render(ctx, u)
		case bManual:
			b.setPrompt(u.ChatID, "classes", id(s.ID))
			b.say(ctx, u.ChatID, "Пришли классы через запятую (англ. имена COCO): person, car, dog", kbCancel())
		default:
			if cls := classFromLabel(text); cls != "" {
				b.applyClassToggle(ctx, u, s, cls)
			}
			b.render(ctx, u)
		}

	case scrAllCls:
		s, ok := b.curStream(u, n)
		if !ok {
			b.goCameras(ctx, u)
			return
		}
		pages := (len(detect.COCONames) + clsPerPage - 1) / clsPerPage
		switch text {
		case bBack:
			b.setNav(u.ChatID, scrClasses, s.ID)
			b.render(ctx, u)
		case bPrev:
			if n.page > 0 {
				n.page--
			}
			b.render(ctx, u)
		case bNext:
			if n.page < pages-1 {
				n.page++
			}
			b.render(ctx, u)
		default:
			if cls := classFromLabel(text); cls != "" {
				b.applyClassToggle(ctx, u, s, cls)
			}
			b.render(ctx, u)
		}

	case scrConf:
		s, ok := b.curStream(u, n)
		if !ok {
			b.goCameras(ctx, u)
			return
		}
		switch text {
		case bBack:
			b.setNav(u.ChatID, scrCamera, s.ID)
			b.render(ctx, u)
		case bManual:
			b.setPrompt(u.ChatID, "conf", id(s.ID))
			b.say(ctx, u.ChatID, "Пришли число 0.05–0.95", kbCancel())
		default:
			if p := atoi(strings.TrimSuffix(text, "%")); p >= 5 && p <= 95 {
				s.Conf = float32(p) / 100
				b.save(ctx, u, s)
			}
			b.setNav(u.ChatID, scrCamera, s.ID)
			b.render(ctx, u)
		}

	case scrRotate:
		s, ok := b.curStream(u, n)
		if !ok {
			b.goCameras(ctx, u)
			return
		}
		m := map[string]string{bRot0: "none", bRot180: "180", bRotCW: "90cw", bRotCCW: "90ccw"}
		if text == bBack {
			b.setNav(u.ChatID, scrCamera, s.ID)
			b.render(ctx, u)
			return
		}
		if mode, ok2 := m[text]; ok2 {
			s.Rotate = mode
			b.save(ctx, u, s)
		}
		b.setNav(u.ChatID, scrCamera, s.ID)
		b.render(ctx, u)

	case scrMute:
		s, ok := b.curStream(u, n)
		if !ok {
			b.goCameras(ctx, u)
			return
		}
		switch text {
		case bBack:
		case bMuteOff:
			_ = b.st.MuteStream(s.ID, time.Unix(0, 0))
		case "15 мин":
			_ = b.st.MuteStream(s.ID, time.Now().Add(15*time.Minute))
		case "1 час":
			_ = b.st.MuteStream(s.ID, time.Now().Add(time.Hour))
		case "8 часов":
			_ = b.st.MuteStream(s.ID, time.Now().Add(8*time.Hour))
		}
		b.setNav(u.ChatID, scrCamera, s.ID)
		b.render(ctx, u)

	case scrDelete:
		s, ok := b.curStream(u, n)
		if !ok {
			b.goCameras(ctx, u)
			return
		}
		if text == bDelYes {
			_ = b.st.DeleteStream(s.ID)
			if b.mgr != nil {
				b.mgr.Sync()
			}
			b.goCameras(ctx, u)
			return
		}
		b.setNav(u.ChatID, scrCamera, s.ID)
		b.render(ctx, u)

	case scrUsers:
		if text == bBack {
			b.setNav(u.ChatID, scrMain, 0)
			b.render(ctx, u)
			return
		}
		if !u.IsAdmin {
			return
		}
		// label like "✅ @bob" (approve) or "🚫 @bob" (block)
		b.toggleUserByLabel(ctx, text)
		b.render(ctx, u)

	default:
		b.setNav(u.ChatID, scrMain, 0)
		b.render(ctx, u)
	}
}

func (b *Bot) routeCamera(ctx context.Context, u store.User, n *navState, text string) {
	s, ok := b.curStream(u, n)
	if !ok {
		b.goCameras(ctx, u)
		return
	}
	switch text {
	case bBack:
		b.goCameras(ctx, u)
	case bEnable, bDisable:
		_ = b.st.SetStreamEnabled(s.ID, text == bEnable)
		if b.mgr != nil {
			b.mgr.Sync()
		}
		b.render(ctx, u)
	case bSnap:
		var jpg []byte
		if b.mgr != nil {
			jpg = b.mgr.Snapshot(s.ID)
		}
		if jpg == nil {
			b.say(ctx, u.ChatID, "Кадр недоступен — поток не запущен.", nil)
			b.render(ctx, u)
			return
		}
		_, _ = b.tg.SendPhoto(ctx, u.ChatID,
			tg.FileField{Field: "photo", Filename: "snap.jpg", Data: jpg},
			fmt.Sprintf("#%d · %s · %s", s.ID, s.Name, time.Now().Format("15:04:05")), nil)
	case bClasses:
		b.setNav(u.ChatID, scrClasses, s.ID)
		b.render(ctx, u)
	case bConf:
		b.setNav(u.ChatID, scrConf, s.ID)
		b.render(ctx, u)
	case bRotate:
		b.setNav(u.ChatID, scrRotate, s.ID)
		b.render(ctx, u)
	case bMute:
		b.setNav(u.ChatID, scrMute, s.ID)
		b.render(ctx, u)
	case bName:
		b.setPrompt(u.ChatID, "rename", id(s.ID))
		b.say(ctx, u.ChatID, "Пришли новое название камеры.", kbCancel())
	case bDelete:
		b.setNav(u.ChatID, scrDelete, s.ID)
		b.render(ctx, u)
	default:
		b.render(ctx, u)
	}
}

func (b *Bot) handlePrompt(ctx context.Context, u store.User, n *navState, text string) {
	kind, data := n.prompt, n.data
	n.prompt, n.data = "", ""

	switch kind {
	case "add_url":
		if validStreamURL(text) != nil {
			b.setPrompt(u.ChatID, "add_url", "")
			b.say(ctx, u.ChatID, "Нужен rtsp:// или http(s)://. Пришли ссылку ещё раз.", kbCancel())
			return
		}
		if b.mgr != nil {
			if err := b.mgr.CheckQuota(u.ChatID); err != nil {
				b.say(ctx, u.ChatID, err.Error(), nil)
				b.goCameras(ctx, u)
				return
			}
		}
		b.say(ctx, u.ChatID, "Проверяю подключение…", nil)
		pctx, cancel := context.WithTimeout(ctx, b.cfg.ProbeTimeout)
		sz, err := media.Probe(pctx, b.cfg.FFprobe, text)
		cancel()
		if err != nil {
			b.setPrompt(u.ChatID, "add_url", "")
			b.say(ctx, u.ChatID, "Не подключился: "+err.Error()+"\nПришли другую ссылку.", kbCancel())
			return
		}
		b.setPrompt(u.ChatID, "add_name", text)
		b.say(ctx, u.ChatID, fmt.Sprintf("Подключился (%d×%d). Пришли название камеры.", sz.W, sz.H), kbCancel())

	case "add_name":
		name := clip(text, 40)
		if name == "" {
			name = "Камера"
		}
		s, err := b.st.AddStream(store.Stream{
			Owner: u.ChatID, Name: name, URL: data, Conf: b.cfg.DefaultConf, Enabled: true,
		})
		if err != nil {
			b.say(ctx, u.ChatID, "Не сохранил: "+err.Error(), nil)
			b.goCameras(ctx, u)
			return
		}
		if b.mgr != nil {
			b.mgr.Sync()
		}
		b.setNav(u.ChatID, scrCamera, s.ID)
		b.say(ctx, u.ChatID, "✅ Камера #"+id(s.ID)+" добавлена.", nil)
		b.render(ctx, u)

	case "rename":
		sid := atoi64(data)
		s, ok, _ := b.st.GetStream(sid)
		if !ok || !b.owns(u, s) {
			return
		}
		s.Name = clip(text, 40)
		b.save(ctx, u, s)
		b.setNav(u.ChatID, scrCamera, sid)
		b.render(ctx, u)

	case "conf":
		sid := atoi64(data)
		s, ok, _ := b.st.GetStream(sid)
		if !ok || !b.owns(u, s) {
			return
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(text), 32)
		if err != nil || f < 0.05 || f > 0.95 {
			b.setPrompt(u.ChatID, "conf", data)
			b.say(ctx, u.ChatID, "Число 0.05–0.95. Ещё раз.", kbCancel())
			return
		}
		s.Conf = float32(f)
		b.save(ctx, u, s)
		b.setNav(u.ChatID, scrCamera, sid)
		b.render(ctx, u)

	case "classes":
		sid := atoi64(data)
		s, ok, _ := b.st.GetStream(sid)
		if !ok || !b.owns(u, s) {
			return
		}
		var cs, bad []string
		for _, c := range strings.Split(text, ",") {
			c = strings.TrimSpace(strings.ToLower(c))
			if c == "" {
				continue
			}
			if en := classFromLabel(c); en != "" {
				cs = append(cs, en)
			} else {
				bad = append(bad, c)
			}
		}
		if len(bad) > 0 {
			b.setPrompt(u.ChatID, "classes", data)
			b.say(ctx, u.ChatID, "Не знаю такие классы: "+strings.Join(bad, ", ")+
				"\nОткрой «📋 Все классы» — там весь список. Пришли ещё раз.", kbCancel())
			return
		}
		if len(cs) == 0 {
			b.setPrompt(u.ChatID, "classes", data)
			b.say(ctx, u.ChatID, "Пусто. Пример: person, car, dog", kbCancel())
			return
		}
		s.Classes = cs
		b.save(ctx, u, s)
		b.setNav(u.ChatID, scrCamera, sid)
		b.render(ctx, u)
	}
}

// --- rendering ---

func (b *Bot) goCameras(ctx context.Context, u store.User) {
	b.setNav(u.ChatID, scrCameras, 0)
	b.render(ctx, u)
}

func (b *Bot) goUsers(ctx context.Context, u store.User) {
	b.setNav(u.ChatID, scrUsers, 0)
	b.render(ctx, u)
}

func (b *Bot) render(ctx context.Context, u store.User) {
	n := b.navOf(u.ChatID)
	text, keys := b.screen(u, n)
	b.say(ctx, u.ChatID, text, tg.ReplyKeyboard(keys...))
}

func (b *Bot) screen(u store.User, n *navState) (string, [][]string) {
	switch n.screen {
	case scrCameras:
		return b.viewCameras(u)
	case scrCamera:
		return b.viewCamera(u, n.stream)
	case scrClasses:
		return b.viewClasses(u, n.stream)
	case scrAllCls:
		return b.viewAllClasses(n.stream, n.page)
	case scrConf:
		return b.viewConf(u, n.stream)
	case scrRotate:
		return "🔄 Поворот кадра", [][]string{{bRot0, bRot180}, {bRotCW, bRotCCW}, {bBack}}
	case scrMute:
		return b.viewMute(u, n.stream)
	case scrDelete:
		return b.viewDelete(u, n.stream)
	case scrUsers:
		return b.viewUsers()
	default:
		return b.viewMain(u)
	}
}

func (b *Bot) viewMain(u store.User) (string, [][]string) {
	keys := [][]string{{bCameras, bAdd}, {bHelp}}
	if u.IsAdmin || u.ChatID == b.cfg.AdminChatID {
		keys = append(keys, []string{bUsers, bStreams})
	}
	return "🎥 Детектор — выбери действие", keys
}

func (b *Bot) viewCameras(u store.User) (string, [][]string) {
	streams, _ := b.st.ListStreamsByOwner(u.ChatID)
	var keys [][]string
	for _, s := range streams {
		mark := "▶️"
		if !s.Enabled {
			mark = "⏸"
		} else if s.Muted() {
			mark = "🔇"
		}
		keys = append(keys, []string{fmt.Sprintf("%s #%d %s", mark, s.ID, s.Name)})
	}
	keys = append(keys, []string{bAdd, bBack})
	txt := "📹 Мои камеры"
	if len(streams) == 0 {
		txt += " — пусто"
	}
	return txt, keys
}

func (b *Bot) viewCamera(u store.User, sid int64) (string, [][]string) {
	s, ok, _ := b.st.GetStream(sid)
	if !ok {
		return "Камера не найдена", [][]string{{bBack}}
	}
	state := "▶️ активна"
	if !s.Enabled {
		state = "⏸ выключена"
	} else if b.mgr != nil && !b.mgr.Running(sid) {
		state = "⏳ подключается"
	}
	if s.Muted() {
		state += " · 🔇 до " + s.MutedUntil.Format("02.01 15:04")
	}
	txt := fmt.Sprintf("#%d · %s\n%s\n\nКлассы: %s\nПорог: %.0f%%\nПоворот: %s",
		s.ID, s.Name, state, strings.Join(s.Classes, ", "), s.Conf*100, rotateRU(s.Rotate))
	toggle := bDisable
	if !s.Enabled {
		toggle = bEnable
	}
	keys := [][]string{
		{toggle, bSnap},
		{bClasses, bConf},
		{bRotate, bMute},
		{bName, bDelete},
		{bBack},
	}
	return txt, keys
}

func (b *Bot) viewClasses(u store.User, sid int64) (string, [][]string) {
	s, ok, _ := b.st.GetStream(sid)
	if !ok {
		return "нет камеры", [][]string{{bBack}}
	}
	has := map[string]bool{}
	for _, c := range s.Classes {
		has[c] = true
	}
	var keys [][]string
	var row []string
	for _, c := range classPalette {
		lbl := c
		if has[c] {
			lbl = "✅ " + c
		}
		row = append(row, lbl)
		if len(row) == 2 {
			keys = append(keys, row)
			row = nil
		}
	}
	if row != nil {
		keys = append(keys, row)
	}
	keys = append(keys, []string{bAllCls, bCatalog}, []string{bManual, bBack})
	return "🏷 Классы #" + id(sid) + "\nСейчас: " + classSummary(s.Classes) +
		"\n\nЧастые — кнопками ниже. «📋 Все классы» — полный список из 80, " +
		"«ℹ️ Описание» — что это за классы.", keys
}

func (b *Bot) viewAllClasses(sid int64, page int) (string, [][]string) {
	s, ok, _ := b.st.GetStream(sid)
	if !ok {
		return "нет камеры", [][]string{{bBack}}
	}
	has := map[string]bool{}
	for _, c := range s.Classes {
		has[c] = true
	}
	pages := (len(detect.COCONames) + clsPerPage - 1) / clsPerPage
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * clsPerPage
	end := start + clsPerPage
	if end > len(detect.COCONames) {
		end = len(detect.COCONames)
	}
	var keys [][]string
	var row []string
	for _, en := range detect.COCONames[start:end] {
		lbl := detect.RussianName(en)
		if has[en] {
			lbl = "✅ " + lbl
		}
		row = append(row, lbl)
		if len(row) == 2 {
			keys = append(keys, row)
			row = nil
		}
	}
	if row != nil {
		keys = append(keys, row)
	}
	keys = append(keys, []string{bPrev, fmt.Sprintf("%d/%d", page+1, pages), bNext}, []string{bBack})
	return fmt.Sprintf("📋 Все классы (стр. %d/%d)\nВыбрано: %s\n(тап — вкл/выкл)",
		page+1, pages, classSummary(s.Classes)), keys
}

// catalogText lists every detectable class, grouped, EN — RU.
func catalogText() string {
	var sb strings.Builder
	sb.WriteString("Модель различает 80 классов (COCO). Английское имя — то, что вводится вручную.\n")
	for _, g := range detect.Groups {
		sb.WriteString("\n<b>" + g.Title + "</b>\n")
		for _, en := range g.Classes {
			ru := detect.RussianName(en)
			if ru == en {
				fmt.Fprintf(&sb, "• %s\n", en)
			} else {
				fmt.Fprintf(&sb, "• <code>%s</code> — %s\n", en, ru)
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func classSummary(cs []string) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = detect.RussianName(c)
	}
	return strings.Join(out, ", ")
}

func (b *Bot) viewConf(u store.User, sid int64) (string, [][]string) {
	s, ok, _ := b.st.GetStream(sid)
	if !ok {
		return "нет камеры", [][]string{{bBack}}
	}
	var row []string
	for _, p := range confPresets {
		row = append(row, fmt.Sprintf("%d%%", p))
	}
	return fmt.Sprintf("🎚 Порог #%d (сейчас %.0f%%)", sid, s.Conf*100),
		[][]string{row, {bManual}, {bBack}}
}

func (b *Bot) viewMute(u store.User, sid int64) (string, [][]string) {
	s, ok, _ := b.st.GetStream(sid)
	cur := "выкл"
	if ok && s.Muted() {
		cur = "до " + s.MutedUntil.Format("02.01 15:04")
	}
	return "🔇 Пауза уведомлений #" + id(sid) + " (сейчас: " + cur + ")",
		[][]string{{"15 мин", "1 час", "8 часов"}, {bMuteOff}, {bBack}}
}

func (b *Bot) viewDelete(u store.User, sid int64) (string, [][]string) {
	s, _, _ := b.st.GetStream(sid)
	return fmt.Sprintf("Удалить камеру #%d «%s»?", sid, s.Name),
		[][]string{{bDelYes}, {bBack}}
}

func (b *Bot) viewUsers() (string, [][]string) {
	users, _ := b.st.ListUsers()
	var sb strings.Builder
	sb.WriteString("👥 Пользователи (тап — сменить статус)\n")
	var keys [][]string
	for _, x := range users {
		name := x.Username
		if name == "" {
			name = id(x.ChatID)
		}
		tag := x.Status
		if x.IsAdmin {
			tag += " · admin"
		}
		fmt.Fprintf(&sb, "\n%s — %s", name, tag)
		if x.IsAdmin {
			continue
		}
		if x.Status == store.StatusApproved {
			keys = append(keys, []string{"🚫 " + name})
		} else {
			keys = append(keys, []string{"✅ " + name})
		}
	}
	keys = append(keys, []string{bBack})
	return sb.String(), keys
}

func (b *Bot) toggleUserByLabel(ctx context.Context, label string) {
	var approve bool
	var name string
	switch {
	case strings.HasPrefix(label, "✅ "):
		approve, name = true, strings.TrimPrefix(label, "✅ ")
	case strings.HasPrefix(label, "🚫 "):
		approve, name = false, strings.TrimPrefix(label, "🚫 ")
	default:
		return
	}
	users, _ := b.st.ListUsers()
	for _, x := range users {
		xn := x.Username
		if xn == "" {
			xn = id(x.ChatID)
		}
		if xn != name || x.IsAdmin {
			continue
		}
		status := store.StatusBlocked
		if approve {
			status = store.StatusApproved
		}
		_ = b.st.SetUserStatus(x.ChatID, status)
		if approve {
			b.setNav(x.ChatID, scrMain, 0)
			t, k := b.viewMain(store.User{ChatID: x.ChatID})
			b.say(ctx, x.ChatID, "Доступ открыт ✅\n\n"+t, tg.ReplyKeyboard(k...))
		} else {
			b.say(ctx, x.ChatID, "Доступ закрыт.", tg.RemoveKeyboard())
		}
		return
	}
}

func (b *Bot) allStreamsText() string {
	streams, _ := b.st.ListStreams()
	if len(streams) == 0 {
		return "Потоков нет."
	}
	var sb strings.Builder
	sb.WriteString("📡 Все потоки\n")
	for _, s := range streams {
		run := "stopped"
		if b.mgr != nil && b.mgr.Running(s.ID) {
			run = "running"
		}
		on := "off"
		if s.Enabled {
			on = "on"
		}
		fmt.Fprintf(&sb, "\n#%d %s — owner %d — %s/%s", s.ID, s.Name, s.Owner, on, run)
	}
	return sb.String()
}

// --- callbacks (event & approval inline buttons only) ---

func (b *Bot) onCallback(ctx context.Context, q *tg.CallbackQuery) {
	ack := func(s string) { _ = b.tg.AnswerCallback(ctx, q.ID, s) }
	action, arg := splitColon(q.Data)

	if action == "approve" || action == "deny" {
		u, ok, _ := b.st.GetUser(q.From.ID)
		if !ok || !u.IsAdmin {
			ack("нет прав")
			return
		}
		target := atoi64(arg)
		status := store.StatusApproved
		if action == "deny" {
			status = store.StatusBlocked
		}
		_ = b.st.SetUserStatus(target, status)
		ack(map[bool]string{true: "одобрено", false: "отклонено"}[action == "approve"])
		if q.Message != nil {
			_ = b.tg.EditReplyMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil)
		}
		if status == store.StatusApproved {
			b.setNav(target, scrMain, 0)
			t, k := b.viewMain(store.User{ChatID: target})
			b.say(ctx, target, "Доступ открыт ✅\n\n"+t, tg.ReplyKeyboard(k...))
		} else {
			b.say(ctx, target, "Доступ закрыт.", tg.RemoveKeyboard())
		}
		return
	}

	// event-message buttons
	u, ok := b.gate(ctx, q.From.ID)
	if !ok {
		ack("нет доступа")
		return
	}
	idStr, param := splitColon(arg)
	sid := atoi64(idStr)
	s, found, _ := b.st.GetStream(sid)
	if !found || !b.owns(u, s) {
		ack("камера не найдена")
		return
	}
	switch action {
	case "snap":
		ack("готовлю кадр…")
		var jpg []byte
		if b.mgr != nil {
			jpg = b.mgr.Snapshot(sid)
		}
		if jpg == nil {
			b.say(ctx, u.ChatID, "Кадр недоступен — поток не запущен.", nil)
			return
		}
		_, _ = b.tg.SendPhoto(ctx, u.ChatID,
			tg.FileField{Field: "photo", Filename: "snap.jpg", Data: jpg},
			fmt.Sprintf("#%d · %s · %s", s.ID, s.Name, time.Now().Format("15:04:05")), nil)
	case "mu":
		mins := atoi(param)
		if mins <= 0 {
			_ = b.st.MuteStream(sid, time.Unix(0, 0))
			ack("уведомления включены")
		} else {
			_ = b.st.MuteStream(sid, time.Now().Add(time.Duration(mins)*time.Minute))
			ack(fmt.Sprintf("пауза %d мин", mins))
		}
	default:
		ack("")
	}
}

// --- event delivery ---

func (b *Bot) OnEvent(s store.Stream, rep pipeline.EventReport) {
	if cur, ok, _ := b.st.GetStream(s.ID); ok {
		s = cur
	}
	if s.Muted() {
		b.logf("bot: event %s stream %d muted, skipping", rep.ID, s.ID)
		return
	}
	lines := make([]notify.ClassLine, len(rep.ClassStats))
	for i, c := range rep.ClassStats {
		lines[i] = notify.ClassLine{Label: c.Label, Count: c.Count, MaxScore: c.MaxScore}
	}
	ev := notify.Event{
		ChatID: s.Owner, StreamName: s.Name, EventID: rep.ID,
		StartedAt: rep.StartedAt, EndedAt: rep.EndedAt, Classes: lines,
		VideoPath: rep.VideoPath, SnapshotPath: rep.SnapshotPath,
		ClipURL: rep.ClipURL, Markup: b.eventKeyboard(s),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := b.nf.SendEvent(ctx, ev); err != nil {
		b.logf("bot: send event %s to %d: %v", rep.ID, s.Owner, err)
	}
}

func (b *Bot) eventKeyboard(s store.Stream) map[string]any {
	top := []tg.InlineButton{{Text: "📸 Кадр", Data: "snap:" + id(s.ID)}}
	if publicURL(b.cfg.PublicURL) {
		top = append(top, tg.InlineButton{Text: "🔗 Веб", URL: fmt.Sprintf("%s/s/%d/preview.jpg", b.cfg.PublicURL, s.ID)})
	}
	return tg.Keyboard(top, []tg.InlineButton{
		{Text: "🔇 15м", Data: fmt.Sprintf("mu:%d:15", s.ID)},
		{Text: "🔇 1ч", Data: fmt.Sprintf("mu:%d:60", s.ID)},
		{Text: "🔇 8ч", Data: fmt.Sprintf("mu:%d:480", s.ID)},
	})
}

// --- helpers ---

func (b *Bot) applyClassToggle(ctx context.Context, u store.User, s store.Stream, cls string) {
	s.Classes = toggle(s.Classes, cls)
	if len(s.Classes) == 0 {
		s.Classes = []string{"person"}
	}
	b.save(ctx, u, s)
}

func (b *Bot) curStream(u store.User, n *navState) (store.Stream, bool) {
	s, ok, _ := b.st.GetStream(n.stream)
	if !ok || !b.owns(u, s) {
		return store.Stream{}, false
	}
	return s, true
}

func (b *Bot) owns(u store.User, s store.Stream) bool { return s.Owner == u.ChatID || u.IsAdmin }

func (b *Bot) save(ctx context.Context, u store.User, s store.Stream) {
	if err := b.st.UpdateStream(s); err != nil {
		b.say(ctx, u.ChatID, "не сохранил: "+err.Error(), nil)
		return
	}
	if b.mgr != nil {
		b.mgr.Sync()
	}
}

func (b *Bot) say(ctx context.Context, chatID int64, text string, markup map[string]any) {
	var err error
	if markup == nil {
		_, err = b.tg.SendText(ctx, chatID, text)
	} else {
		_, err = b.tg.SendMessage(ctx, chatID, text, markup)
	}
	if err != nil {
		b.logf("bot: send to %d: %v", chatID, err)
	}
}

func (b *Bot) sayHTML(ctx context.Context, chatID int64, text string) {
	if _, err := b.tg.SendMessage(ctx, chatID, text, nil); err != nil {
		b.logf("bot: send to %d: %v", chatID, err)
	}
}

func kbCancel() map[string]any { return tg.ReplyKeyboard([]string{bBack}) }

func rotateRU(m string) string {
	switch m {
	case "90cw":
		return "90° ↻"
	case "90ccw":
		return "90° ↺"
	case "180":
		return "180°"
	default:
		return "нет"
	}
}

func parseHashID(s string) int64 {
	i := strings.IndexByte(s, '#')
	if i < 0 {
		return 0
	}
	j := i + 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	v, _ := strconv.ParseInt(s[i+1:j], 10, 64)
	return v
}

func toggle(list []string, v string) []string {
	out := make([]string, 0, len(list)+1)
	found := false
	for _, x := range list {
		if x == v {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		out = append(out, v)
	}
	return out
}

func splitColon(s string) (a, c string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func validStreamURL(u string) error {
	l := strings.ToLower(strings.TrimSpace(u))
	for _, p := range []string{"rtsp://", "rtsps://", "http://", "https://"} {
		if strings.HasPrefix(l, p) {
			return nil
		}
	}
	return fmt.Errorf("нужен rtsp:// или http(s)://")
}

func publicURL(u string) bool {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}
	for _, bad := range []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"} {
		if strings.Contains(u, bad) {
			return false
		}
	}
	return true
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func id(v int64) string     { return strconv.FormatInt(v, 10) }
func atoi64(s string) int64 { v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return v }
func atoi(s string) int     { v, _ := strconv.Atoi(strings.TrimSpace(s)); return v }

func helpText(admin bool) string {
	s := `Управление — кнопками на месте клавиатуры.

📹 Камеры — список; по камере: вкл/выкл, кадр сейчас,
классы, порог, поворот, пауза уведомлений, имя, удалить.
➕ Добавить — пришлёшь RTSP-ссылку и название.

Под событием: 📸 Кадр и пауза 15м/1ч/8ч.`
	if admin {
		s += "\n\n👥 Пользователи — одобрять/блокировать.\n📡 Все потоки — обзор."
	}
	return s
}
