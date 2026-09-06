BIN := detector

.PHONY: build run tidy clean tg-setup docker

TG_TOKEN ?= $(TELEGRAM_BOT_TOKEN)
TG_CHAT ?= $(TELEGRAM_CHAT_ID)

tidy:
	go mod tidy

build:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/detector

run: build
	./$(BIN) -config config.yaml

docker:
	docker build -t detector .

# List chats that know the bot (copy chat_id into config.yaml).
# Add SEND=1 to fire a test message to TG_CHAT.
tg-setup:
	go run ./cmd/tg-setup -token "$(TG_TOKEN)" -chat "$(TG_CHAT)" $(if $(SEND),-send,)

clean:
	rm -f $(BIN)
