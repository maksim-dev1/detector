// Package tg is a minimal Telegram Bot API client: JSON calls, multipart
// uploads, long polling, and the handful of methods the detector bot needs.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	token string
	hc    *http.Client
}

func New(token string) *Client {
	return &Client{token: token, hc: &http.Client{Timeout: 90 * time.Second}}
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// Call invokes a Bot API method with JSON params and decodes result into out
// (out may be nil).
func (c *Client) Call(ctx context.Context, method string, params map[string]any, out any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.token, method), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

// FileField is one file part in a multipart upload.
type FileField struct {
	Field    string
	Filename string
	Data     []byte
}

// Upload invokes a method with multipart/form-data (for sending media).
func (c *Client) Upload(ctx context.Context, method string, fields map[string]string, files []FileField, out any) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	for _, f := range files {
		part, err := w.CreateFormFile(f.Field, f.Filename)
		if err != nil {
			return err
		}
		if _, err := part.Write(f.Data); err != nil {
			return err
		}
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://api.telegram.org/bot%s/%s", c.token, method), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var r apiResp
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("telegram %s: bad response: %s", req.URL.Path, truncate(b, 200))
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %d %s", req.URL.Path, r.ErrorCode, r.Description)
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// --- typed helpers ---

type User struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
	First    string `json:"first_name"`
}

type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.Call(ctx, "getMe", nil, &u)
	return u, err
}

// GetUpdates long-polls starting at offset. timeout is the server-side wait.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var ups []Update
	err := c.Call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "callback_query"},
	}, &ups)
	return ups, err
}

// InlineButton is one inline keyboard button.
type InlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Keyboard builds an inline_keyboard reply markup from rows of buttons.
func Keyboard(rows ...[]InlineButton) map[string]any {
	return map[string]any{"inline_keyboard": rows}
}

// ReplyKeyboard builds a persistent custom keyboard that replaces the user's
// normal keyboard; tapping a key sends its label as a plain message.
func ReplyKeyboard(rows ...[]string) map[string]any {
	return map[string]any{
		"keyboard":        rows,
		"resize_keyboard": true,
		"is_persistent":   true,
	}
}

// RemoveKeyboard hides a custom keyboard.
func RemoveKeyboard() map[string]any {
	return map[string]any{"remove_keyboard": true}
}

// SendText sends a plain-text message (no HTML parsing).
func (c *Client) SendText(ctx context.Context, chatID int64, text string) (Message, error) {
	var m Message
	err := c.Call(ctx, "sendMessage", map[string]any{
		"chat_id": chatID, "text": text, "disable_web_page_preview": true,
	}, &m)
	return m, err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, markup map[string]any) (Message, error) {
	p := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if markup != nil {
		p["reply_markup"] = markup
	}
	var m Message
	err := c.Call(ctx, "sendMessage", p, &m)
	return m, err
}

func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	return c.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// EditText replaces a message's text and inline keyboard (HTML parse mode).
func (c *Client) EditText(ctx context.Context, chatID, msgID int64, text string, markup map[string]any) error {
	p := map[string]any{
		"chat_id": chatID, "message_id": msgID, "text": text,
		"parse_mode": "HTML", "disable_web_page_preview": true,
	}
	if markup != nil {
		p["reply_markup"] = markup
	}
	err := c.Call(ctx, "editMessageText", p, nil)
	// editing to identical content is a harmless 400
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

func (c *Client) EditReplyMarkup(ctx context.Context, chatID, msgID int64, markup map[string]any) error {
	p := map[string]any{"chat_id": chatID, "message_id": msgID}
	if markup != nil {
		p["reply_markup"] = markup
	}
	return c.Call(ctx, "editMessageReplyMarkup", p, nil)
}

func (c *Client) DeleteMessage(ctx context.Context, chatID, msgID int64) error {
	return c.Call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": msgID}, nil)
}

// SendVideo uploads a video with an optional custom thumbnail, HTML caption and
// inline keyboard. Returns the sent message.
func (c *Client) SendVideo(ctx context.Context, chatID int64, video FileField, thumb *FileField, caption string, markup map[string]any) (Message, error) {
	fields := map[string]string{
		"chat_id":            strconv.FormatInt(chatID, 10),
		"caption":            caption,
		"parse_mode":         "HTML",
		"supports_streaming": "true",
	}
	files := []FileField{{Field: "video", Filename: video.Filename, Data: video.Data}}
	if thumb != nil {
		fields["thumbnail"] = "attach://thumb"
		files = append(files, FileField{Field: "thumb", Filename: thumb.Filename, Data: thumb.Data})
	}
	if markup != nil {
		mb, _ := json.Marshal(markup)
		fields["reply_markup"] = string(mb)
	}
	var m Message
	err := c.Upload(ctx, "sendVideo", fields, files, &m)
	return m, err
}

// SendPhoto uploads a photo with HTML caption and optional keyboard.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, photo FileField, caption string, markup map[string]any) (Message, error) {
	fields := map[string]string{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"caption":    caption,
		"parse_mode": "HTML",
	}
	if markup != nil {
		mb, _ := json.Marshal(markup)
		fields["reply_markup"] = string(mb)
	}
	var m Message
	err := c.Upload(ctx, "sendPhoto", fields, []FileField{{Field: "photo", Filename: photo.Filename, Data: photo.Data}}, &m)
	return m, err
}
