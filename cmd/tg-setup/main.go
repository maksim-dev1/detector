// tg-setup helps wire up the Telegram bot: it lists the chats that have talked
// to the bot (so you can copy the chat_id) and can send a test message.
//
//	go run ./cmd/tg-setup -token 123:abc                 # list chats
//	go run ./cmd/tg-setup -token 123:abc -chat 456 -send # send a test message
//
// Token also read from $TELEGRAM_BOT_TOKEN, chat from $TELEGRAM_CHAT_ID.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	token := flag.String("token", os.Getenv("TELEGRAM_BOT_TOKEN"), "bot token from @BotFather")
	chat := flag.String("chat", os.Getenv("TELEGRAM_CHAT_ID"), "chat id for -send")
	send := flag.Bool("send", false, "send a test message to -chat")
	flag.Parse()

	if *token == "" {
		log.Fatal("need -token or $TELEGRAM_BOT_TOKEN")
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	api := func(method string, form url.Values) map[string]any {
		u := fmt.Sprintf("https://api.telegram.org/bot%s/%s", *token, method)
		resp, err := hc.PostForm(u, form)
		if err != nil {
			log.Fatalf("%s: %v", method, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		if err := json.Unmarshal(b, &out); err != nil {
			log.Fatalf("%s: bad response: %s", method, b)
		}
		if ok, _ := out["ok"].(bool); !ok {
			log.Fatalf("%s: telegram error: %s", method, b)
		}
		return out
	}

	me := api("getMe", nil)
	if r, ok := me["result"].(map[string]any); ok {
		fmt.Printf("bot: @%v (%v)\n", r["username"], r["first_name"])
	}

	if *send {
		if *chat == "" {
			log.Fatal("need -chat or $TELEGRAM_CHAT_ID for -send")
		}
		api("sendMessage", url.Values{
			"chat_id": {*chat},
			"text":    {"✅ detector: тестовое сообщение, связь есть"},
		})
		fmt.Printf("sent test message to %s\n", *chat)
		return
	}

	upd := api("getUpdates", url.Values{"timeout": {"0"}})
	results, _ := upd["result"].([]any)
	if len(results) == 0 {
		fmt.Println("\nНет сообщений. Напиши что-нибудь боту в личку (или добавь в группу")
		fmt.Println("и упомяни его), потом запусти снова — покажет chat_id.")
		return
	}
	seen := map[string]bool{}
	fmt.Println("\nчаты, знающие бота:")
	for _, it := range results {
		m, _ := it.(map[string]any)
		var msg map[string]any
		for _, k := range []string{"message", "channel_post", "my_chat_member"} {
			if v, ok := m[k].(map[string]any); ok {
				msg = v
				break
			}
		}
		if msg == nil {
			continue
		}
		c, _ := msg["chat"].(map[string]any)
		if c == nil {
			continue
		}
		id := fmt.Sprintf("%v", c["id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(fmt.Sprintf("%v %v", c["first_name"], c["last_name"]))
		if t, _ := c["title"].(string); t != "" {
			name = t
		}
		fmt.Printf("  chat_id=%s  type=%v  %s\n", id, c["type"], name)
	}
	fmt.Println("\nВпиши chat_id в config.yaml (notify.telegram.chat_id) или $TELEGRAM_CHAT_ID.")
}
