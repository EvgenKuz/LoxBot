package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"desolidify.ru/lox-bot/internal/storage"
)

// waitTime is a var, not a const, so tests can shorten it to zero.
var waitTime = 2 * time.Second

func sendMessage(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string) bool {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
	if err != nil {
		log.Error("Failed to send message to chat", "chat_id", chatID, "err", err.Error())
		return false
	}

	return true
}

func handlerStart(log *slog.Logger) func(ctx context.Context, b *bot.Bot, update *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		text := "Привет! Это бот сделан, чтобы добавлять его в чат и определять, кто есть \"Лох дня!\""
		sendMessage(ctx, b, log, update.Message.Chat.ID, text)
	}
}

func handlerRegister(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		userId := update.Message.From.ID
		chatId := update.Message.Chat.ID

		exists, err := storage.UserExists(db, userId, chatId)
		if err != nil {
			log.Error("DB failed to check if user exists", "user_id", userId, "chat_id", chatId, "err", err.Error())
			return
		}

		if !exists {
			err = storage.AddUser(db, userId, chatId, update.Message.From.FirstName, update.Message.From.LastName)
			if err != nil {
				log.Error("DB failed to add user", "user_id", userId, "chat_id", chatId, "err", err.Error())
				return
			}

			_, err = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatId,
				Text:   fmt.Sprintf("Ты в игре, %s!", update.Message.From.FirstName),
				ReplyParameters: &models.ReplyParameters{
					MessageID: update.Message.ID,
					ChatID:    chatId,
				},
			})
			if err != nil {
				log.Error("Failed to send message to chat", "user_id", userId, "chat_id", chatId, "err", err.Error())
			}

			return
		}

		active, err := storage.IsActive(db, userId, chatId)
		if err != nil {
			log.Error("DB failed to check if user is active", "user_id", userId, "chat_id", chatId, "err", err.Error())
			return
		}

		if !active {
			err = storage.ActivateUser(db, userId, chatId)
			if err != nil {
				log.Error("DB failed to activate user", "user_id", userId, "chat_id", chatId, "err", err.Error())
				return
			}
			_, err = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatId,
				Text:   fmt.Sprintf("Ты в игре, %s!", update.Message.From.FirstName),
				ReplyParameters: &models.ReplyParameters{
					MessageID: update.Message.ID,
					ChatID:    chatId,
				},
			})
			if err != nil {
				log.Error("Failed to send message to chat", "user_id", userId, "chat_id", chatId, "err", err.Error())
			}
			return
		}

		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatId,
			Text:   "Ты уже в игре...",
			ReplyParameters: &models.ReplyParameters{
				MessageID: update.Message.ID,
				ChatID:    chatId,
			},
		})
		if err != nil {
			log.Error("Failed to send message to chat", "user_id", userId, "chat_id", chatId, "err", err.Error())
		}
		return
	}
}

func handleUnregister(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		userId := update.Message.From.ID
		chatId := update.Message.Chat.ID

		exists, err := storage.UserExists(db, userId, chatId)
		if err != nil {
			log.Error("DB failed to check if user exists", "user_id", userId, "chat_id", chatId, "err", err.Error())
			return
		}
		if !exists {
			_, err = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatId,
				Text:   "Ты и так не участвуешь, дебил!",
				ReplyParameters: &models.ReplyParameters{
					MessageID: update.Message.ID,
					ChatID:    chatId,
				},
			})
			if err != nil {
				log.Error("Failed to send message to chat", "user_id", userId, "chat_id", chatId, "err", err.Error())
			}
			return
		}

		active, err := storage.IsActive(db, userId, chatId)
		if err != nil {
			log.Error("Failed to check if user is active", "user_id", userId, "chat_id", chatId, "err", err.Error())
			return
		}
		if !active {
			_, err = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: chatId,
				Text:   "Ты и так не участвуешь, дебил!",
				ReplyParameters: &models.ReplyParameters{
					MessageID: update.Message.ID,
					ChatID:    chatId,
				},
			})
			if err != nil {
				log.Error("Failed to send message to chat", "user_id", userId, "chat_id", chatId, "err", err.Error())
			}
			return
		}

		err = storage.DeactivateUser(db, userId, chatId)
		if err != nil {
			log.Error("DB failed to deactivate user", "user_id", userId, "chat_id", chatId, "err", err.Error())
			return
		}

		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatId,
			Text:   fmt.Sprintf("Ты больше не в игре, %s(", update.Message.From.FirstName),
			ReplyParameters: &models.ReplyParameters{
				MessageID: update.Message.ID,
				ChatID:    chatId,
			},
		})
		if err != nil {
			log.Error("Failed to send message to chat", "user_id", userId, "chat_id", chatId, "err", err.Error())
		}
		return
	}
}

func handleRun(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		// Check for an already-decided result first, so it can still be shown
		// even if every player has since left the game.
		game, err := storage.GetTodayGame(db, chatId)
		if err != nil {
			log.Error("Failed to get today's game", "chat_id", chatId, "err", err.Error())
			return
		}
		if game.WinTelegramId != nil {
			firstName, lastName := resolveName(ctx, b, db, log, *game.WinTelegramId, chatId)
			var message string
			if lastName == "" {
				message = fmt.Sprintf("Сегодня Красавчик Дня %s!", firstName)
			} else {
				message = fmt.Sprintf("Сегодня Красавчик Дня %s %s!", firstName, lastName)
			}

			sendMessage(ctx, b, log, chatId, message)
			return
		}

		players, err := storage.GetAllPlayers(db, chatId)
		if err != nil {
			log.Error("DB failed to get players in chat", "chat_id", chatId, "err", err.Error())
			return
		}
		if len(players) == 0 {
			text := "Нет активных игроков("
			sendMessage(ctx, b, log, chatId, text)
			return
		}

		var chosenPlayerId int64
		for {
			randomIndex := rand.IntN(len(players))
			chosenPlayerId = players[randomIndex]
			if len(players) == 1 || game.LoxTelegramId == nil || chosenPlayerId != *game.LoxTelegramId {
				break
			}
		}
		set, err := storage.AddWinner(db, chosenPlayerId, chatId)
		if err != nil {
			log.Error("DB failed to add winner", "user_id", chosenPlayerId, "chat_id", chatId, "err", err.Error())
			return
		}
		if !set {
			// Another /run set the winner first; that call announces it.
			return
		}

		firstName, lastName := resolveName(ctx, b, db, log, chosenPlayerId, chatId)
		var message string
		if lastName == "" {
			message = fmt.Sprintf("Сегодня Красавчик дня...\n\n%s!!!", firstName)
		} else {
			message = fmt.Sprintf("Сегодня Красавчик дня...\n\n%s %s!!!", firstName, lastName)
		}

		sendMessage(ctx, b, log, chatId, "Смотрю на ваши лица...")
		time.Sleep(waitTime)
		sendMessage(ctx, b, log, chatId, "Чекаю сообщения...")
		time.Sleep(waitTime)
		sendMessage(ctx, b, log, chatId, "Готовлю губы для поцелуя...")
		time.Sleep(waitTime)
		sendMessage(ctx, b, log, chatId, message)
		return
	}
}

func handleLoxRun(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		// Check for an already-decided result first, so it can still be shown
		// even if every player has since left the game.
		game, err := storage.GetTodayGame(db, chatId)
		if err != nil {
			log.Error("Failed to get today's game", "chat_id", chatId, "err", err.Error())
			return
		}
		if game.LoxTelegramId != nil {
			firstName, lastName := resolveName(ctx, b, db, log, *game.LoxTelegramId, chatId)
			var message string
			if lastName == "" {
				message = fmt.Sprintf("Сегодня Лох Дня %s!", firstName)
			} else {
				message = fmt.Sprintf("Сегодня Лох Дня %s %s!", firstName, lastName)
			}

			sendMessage(ctx, b, log, chatId, message)
			return
		}

		players, err := storage.GetAllPlayers(db, chatId)
		if err != nil {
			log.Error("DB failed to get players in chat", "chat_id", chatId, "err", err.Error())
			return
		}
		if len(players) == 0 {
			sendMessage(ctx, b, log, chatId, "Нет активных игроков(")
			return
		}

		var chosenPlayerId int64
		for {
			randomIndex := rand.IntN(len(players))
			chosenPlayerId = players[randomIndex]
			if len(players) == 1 || game.WinTelegramId == nil || chosenPlayerId != *game.WinTelegramId {
				break
			}
		}
		set, err := storage.AddLox(db, chosenPlayerId, chatId)
		if err != nil {
			log.Error("DB failed to add lox", "user_id", chosenPlayerId, "chat_id", chatId, "err", err.Error())
			return
		}
		if !set {
			// Another /lox set the lox first; that call announces it.
			return
		}

		firstName, lastName := resolveName(ctx, b, db, log, chosenPlayerId, chatId)
		var message string
		if lastName == "" {
			message = fmt.Sprintf("Сегодня Лох дня...\n\n%s!!!", firstName)
		} else {
			message = fmt.Sprintf("Сегодня Лох дня...\n\n%s %s!!!", firstName, lastName)
		}

		sendMessage(ctx, b, log, chatId, "Сру в штаны...")
		time.Sleep(waitTime)
		sendMessage(ctx, b, log, chatId, "Надеваю ведро на голову...")
		time.Sleep(waitTime)
		sendMessage(ctx, b, log, chatId, "Готовлюсь харкаться...")
		time.Sleep(waitTime)
		sendMessage(ctx, b, log, chatId, message)
		return
	}
}

func getPlayerInfo(ctx context.Context, b *bot.Bot, log *slog.Logger, userId int64, chatId int64) (*models.User, bool) {
	player, err := b.GetChatMember(ctx, &bot.GetChatMemberParams{
		ChatID: chatId,
		UserID: userId,
	})
	if err != nil {
		log.Error("Failed to get player info from Telegram", "user_id", userId, "chat_id", chatId, "err", err.Error())
		return nil, false
	}

	if player.Member != nil {
		return player.Member.User, true
	} else if player.Owner != nil {
		return player.Owner.User, true
	} else if player.Administrator != nil {
		return &player.Administrator.User, true
	} else if player.Restricted != nil {
		return player.Restricted.User, true
	} else if player.Banned != nil {
		return player.Banned.User, true
	} else if player.Left != nil {
		return player.Left.User, true
	}

	return nil, false
}

// resolveName always yields a usable name so a command never aborts silently:
// the stored name, else a live Telegram lookup (needs admin), else a
// placeholder. A placeholder self-heals once the player next uses the bot.
func resolveName(ctx context.Context, b *bot.Bot, db *sql.DB, log *slog.Logger, userId int64, chatId int64) (firstName, lastName string) {
	fn, ln, found, err := storage.GetUserNameDB(db, userId, chatId)
	if err != nil {
		log.Error("DB failed to get stored user name", "user_id", userId, "chat_id", chatId, "err", err.Error())
	}
	if found && fn != "" {
		return fn, ln
	}

	if player, got := getPlayerInfo(ctx, b, log, userId, chatId); got {
		return player.FirstName, player.LastName
	}

	return fmt.Sprintf("Игрок #%04d", abs(userId)%10000), ""
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func handleStats(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		results, err := storage.GetWinners(db, chatId)
		if err != nil {
			log.Error("DB failed to get winners in chat", "chat_id", chatId, "err", err.Error())
		}

		var message strings.Builder
		message.WriteString("Был Красавчиком Дня:\n")
		for _, result := range results {
			firstName, lastName := resolveName(ctx, b, db, log, result.UserId, chatId)

			var line string
			if lastName == "" {
				line = fmt.Sprintf("\n%s - %d раз", firstName, result.Lucky)
			} else {
				line = fmt.Sprintf("\n%s %s - %d раз", firstName, lastName, result.Lucky)
			}
			message.WriteString(line)
		}

		sendMessage(ctx, b, log, chatId, message.String())
	}
}

func handleLoxStats(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		results, err := storage.GetLoxes(db, chatId)
		if err != nil {
			log.Error("DB failed to get loxes in chat", "chat_id", chatId, "err", err.Error())
		}

		var message strings.Builder
		message.WriteString("Был Лохом Дня:\n")
		for _, result := range results {
			firstName, lastName := resolveName(ctx, b, db, log, result.UserId, chatId)

			var line string
			if lastName == "" {
				line = fmt.Sprintf("\n%s - %d раз", firstName, result.Lucky)
			} else {
				line = fmt.Sprintf("\n%s %s - %d раз", firstName, lastName, result.Lucky)
			}
			message.WriteString(line)
		}

		sendMessage(ctx, b, log, chatId, message.String())
	}
}

// NameMiddleware keeps stored names fresh by recording the sender's name on
// each message. It only updates already-registered users; it never creates one.
func NameMiddleware(db *sql.DB, log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if update.Message != nil && update.Message.From != nil {
				from := update.Message.From
				if err := storage.UpdateUserName(db, from.ID, update.Message.Chat.ID, from.FirstName, from.LastName); err != nil {
					log.Error("Failed to update stored user name", "user_id", from.ID, "chat_id", update.Message.Chat.ID, "err", err.Error())
				}
			}
			next(ctx, b, update)
		}
	}
}

// handleAddFake adds a synthetic player named by the command argument, e.g.
// "/addfake Вася", so one tester can fill a chat.
func handleAddFake(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		name := ""
		if parts := strings.SplitN(update.Message.Text, " ", 2); len(parts) == 2 {
			name = strings.TrimSpace(parts[1])
		}
		if name == "" {
			sendMessage(ctx, b, log, chatId, "Использование: /addfake <имя>")
			return
		}

		id, err := storage.AddFakeUser(db, chatId, name, "")
		if err != nil {
			log.Error("DB failed to add fake user", "chat_id", chatId, "err", err.Error())
			return
		}

		sendMessage(ctx, b, log, chatId, fmt.Sprintf("Добавлен тестовый игрок %s (id %d)", name, id))
	}
}

func handleNextDay(log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		date := storage.AdvanceDay()
		sendMessage(ctx, b, log, update.Message.Chat.ID, fmt.Sprintf("Теперь \"сегодня\": %s", date))
	}
}

func handleResetDay(log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		date := storage.ResetDay()
		sendMessage(ctx, b, log, update.Message.Chat.ID, fmt.Sprintf("Дата сброшена на сегодня: %s", date))
	}
}

func RegisterHandlers(b *bot.Bot, db *sql.DB, log *slog.Logger, debug bool) {
	re := regexp.MustCompile("^/start(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handlerStart(log))
	re = regexp.MustCompile("^/register(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handlerRegister(db, log))
	re = regexp.MustCompile("^/leave(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleUnregister(db, log))
	re = regexp.MustCompile("^/run(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleRun(db, log))
	re = regexp.MustCompile("^/lox(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleLoxRun(db, log))
	re = regexp.MustCompile("^/stats(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleStats(db, log))
	re = regexp.MustCompile("^/loxstats(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleLoxStats(db, log))
	re = regexp.MustCompile("^/dicestats(?:@.+)?$")
	b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleDiceStats(db, log))
	b.RegisterHandlerMatchFunc(isOwnDiceRoll, handleDice(db, log))

	// Testing-only commands, gated so they never reach production.
	if debug {
		re = regexp.MustCompile(`^/addfake(?:@\S+)?(?:\s.*)?$`)
		b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleAddFake(db, log))
		re = regexp.MustCompile("^/nextday(?:@.+)?$")
		b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleNextDay(log))
		re = regexp.MustCompile("^/resetday(?:@.+)?$")
		b.RegisterHandlerRegexp(bot.HandlerTypeMessageText, re, handleResetDay(log))
	}
}
