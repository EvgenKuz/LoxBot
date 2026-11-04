package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-telegram/bot"
	"github.com/google/uuid"

	"desolidify.ru/lox-bot/internal/storage"
	"desolidify.ru/lox-bot/internal/telegram"
)

// maxUpdateBytes caps a webhook request body. Telegram updates are a few KB;
// the cap only matters if the secret token ever leaks.
const maxUpdateBytes = 1 << 20

func main() {
	os.Exit(run())
}

// run returns the process exit code. It is split from main so deferred cleanup
// (closing the database) still happens before os.Exit.
func run() int {
	// Docker stops containers with SIGTERM, so listen for it as well as Ctrl+C.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log := slog.Default()
	slog.SetLogLoggerLevel(slog.LevelInfo)
	db, err := storage.CreateConnection()
	if err != nil {
		log.Error("Failed to connect to database", "err", err.Error())
		return 1
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
		return 1
	}
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		log.Error("WEBHOOK_URL environment variable not set")
		return 1
	}
	apiKey := uuid.New().String()

	b, err := bot.New(botKey,
		bot.WithWebhookSecretToken(apiKey),
		bot.WithMiddlewares(telegram.NameMiddleware(db, log)),
	)
	if err != nil || b == nil {
		log.Error("Failed to create bot", "err", err)
		return 1
	}
	debugCommands := os.Getenv("DEBUG_COMMANDS") != ""
	telegram.RegisterHandlers(b, db, log, debugCommands)

	_, err = b.SetWebhook(ctx, &bot.SetWebhookParams{
		URL:         webhookURL,
		SecretToken: apiKey,
	})
	if err != nil {
		log.Error("Failed to set webhook", "err", err.Error())
		return 1
	}

	// Timeouts stop slow or idle clients from holding connections open forever.
	// There is deliberately no WriteTimeout: the handler waits for a free worker
	// before answering, and cutting that response off would make Telegram resend
	// an update that was already accepted.
	server := &http.Server{
		Addr:              ":1080",
		Handler:           http.MaxBytesHandler(b.WebhookHandler(), maxUpdateBytes),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			cancel()
		}
	}()

	log.Info("Bot is now running.")
	b.StartWebhook(ctx)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("Failed to shut down webhook server", "err", err.Error())
	}

	select {
	case err := <-serverErr:
		log.Error("Webhook server failed", "err", err.Error())
		return 1
	default:
		log.Info("Bot stopped.")
		return 0
	}
}
