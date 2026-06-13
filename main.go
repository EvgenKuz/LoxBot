package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"

	"github.com/go-telegram/bot"
	"github.com/google/uuid"

	"desolidify.ru/lox-bot/internal/storage"
	"desolidify.ru/lox-bot/internal/telegram"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log := slog.Default()
	slog.SetLogLoggerLevel(slog.LevelInfo)
	db, err := storage.CreateConnection()
	if err != nil {
		log.Error("Failed to connect to database", "err", err.Error())
		return
	}
	defer func(db *sql.DB) {
		err := db.Close()
		if err != nil {
			log.Error("Failed to properly close database", "err", err.Error())
		}
	}(db)

	botKey := os.Getenv("BOT_KEY")
	if botKey == "" {
		log.Error("BOT_KEY environment variable not set")
		return
	}
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		log.Error("WEBHOOK_URL environment variable not set")
		return
	}
	apiKey := uuid.New().String()

	b, err := bot.New(botKey, bot.WithWebhookSecretToken(apiKey))
	if err != nil || b == nil {
		log.Error("Failed to create bot", "err", err)
		return
	}
	telegram.RegisterHandlers(b, db, log)

	_, err = b.SetWebhook(ctx, &bot.SetWebhookParams{
		URL:         webhookURL,
		SecretToken: apiKey,
	})
	if err != nil {
		log.Error("Failed to set webhook", "err", err.Error())
		return
	}

	go func() {
		if err = http.ListenAndServe(":1080", b.WebhookHandler()); err != nil {
			log.Error("Failed to start server", "err", err.Error())
		}
		panic(err)
	}()

	log.Info("Bot is now running.")
	b.StartWebhook(ctx)
}
