# detector

Сервис детекции по RTSP: Telegram-бот, в который одобренные пользователи
добавляют свои камеры и получают события (клип + фото, всё одним сообщением).

```
Telegram-бот ── /add /list /rotate ...        SQLite (users, streams)
      │                                              │
      ▼                                              ▼
   manager ── по одному pipeline на камеру ◄──── синк со стором (20с)
      │
 pipeline: RTSP ─ ffmpeg ─ RGB640 ─ YOLO-сервис (POST /detect) ─ event engine
                  └ .ts сегменты 2с ─ кольцевой буфер (пре-запись)
 событие CLOSE → сборка клипа [start-pre..end+post] с burn-in рамками
              → лучший annotated-кадр → ОДНО сообщение владельцу:
                sendVideo(клип + thumbnail + caption + кнопки)
```

- инференс — внешний YOLO-сервис по HTTP (`POST /detect`, multipart-кадр → JSON
  боксы). Свой YOLO на nexus: `/opt/compose/yolo` (ultralytics `yolo11s`, CUDA)
- бинарь чистый Go, без cgo — `CGO_ENABLED=0`
- всё видео — `ffmpeg`, OpenCV не нужен
- рамки детекций: свой стабильный цвет на класс + `%` уверенности (в превью,
  на фото и burn-in на клипе; текст на клипе — если в системе есть TTF-шрифт)

## Установка

Требуется: Go 1.25+, ffmpeg/ffprobe, доступный YOLO-сервис.

```bash
cp config.example.yaml config.yaml
$EDITOR config.yaml               # bot.token, bot.admin_chat_id, detect.yolo_url
make run
```

Docker: `docker build -t detector .` — образ несёт ffmpeg + шрифт, конфиг берёт
из `config.docker.yaml`, секреты через env (`BOT_TOKEN`, `ADMIN_CHAT_ID`,
`YOLO_URL`), данные в volume `/data`.

## Бот

```
@BotFather → /newbot                          # bot.token
make tg-setup TG_TOKEN=123:abc                 # твой chat_id → bot.admin_chat_id
```

Бот шлёт только одобренным. `/start` от нового пользователя → заявка админу с
кнопками ✅/❌.

Управление — **через меню кнопок**, единственная команда `/start` (открывает меню).

- **📹 Мои камеры** → список → камера: вкл/выкл, 📸 кадр, 🏷 классы (тумблеры +
  ручной ввод), 🎚 порог, 🔄 поворот, 🔇 пауза (15м/1ч/8ч/снять), ✏️ имя, 🗑 удалить
- **➕ Добавить** → пришли RTSP-ссылку, затем название (подключение проверяется)
- **админ**: 👥 Пользователи (одобрить/заблокировать), 📡 Все потоки

Свободный текст бот принимает только когда меню его просит (ссылка, имя,
список классов, число порога); всё остальное открывает меню.

Событие приходит одним `sendVideo`: клип с рамками, обложка — annotated-кадр,
подпись с длительностью / классами / пиковой уверенностью, кнопки `📸 Кадр` и
(если задан `server.public_url`) `🔗 Веб`.

## Конфиг — ключевое

| Поле | Смысл |
|---|---|
| `bot.token` / `bot.admin_chat_id` | обязательны (или env `BOT_TOKEN` / `ADMIN_CHAT_ID`) |
| `store.path` | SQLite-файл: пользователи и потоки, переживает рестарт |
| `detect.fps` | кадров/сек через YOLO на каждый поток |
| `detect.use_cuda` | CUDA execution provider (иначе CPU) |
| `event.pre/post_seconds` | сколько до/после захватить в клип |
| `recording.dir` | `recordings/<stream_id>/{segments,events}` |
| `limits.max_streams_per_user` / `_total` | лимиты на `/add` |
| `server.public_url` | публичный `https://` для кнопки «🔗 Веб» и ссылок на клипы |
| `seed` | на первом запуске добавить одну камеру (владелец — админ) |

## HTTP (`server.enabled`)

Минимальный, read-only: `GET /healthz`, `GET /s/<id>/preview.jpg` (живой кадр с
рамками), `GET /s/<id>/clips/<file>.mp4`.

## Проверка модели без камеры

```bash
go run ./cmd/detect-image -yolo http://192.168.0.155:8000 -conf 0.25 -out out.jpg photo.jpg
```

## Известные ограничения

- границы клипа с точностью до сегмента (± `segment_seconds`) при `rotate: none`
  (fast copy); при повороте клип перекодируется и режется точно
- текст (`%` уверенности) на видео-клипе — только если ffmpeg собран с `drawtext`
  и в системе есть TTF-шрифт; рамки цветом класса — всегда
- OSD-время камеры и системное время должны примерно совпадать
