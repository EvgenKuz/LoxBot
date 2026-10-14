package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"desolidify.ru/lox-bot/internal/storage"
)

// diceStreakWindow is how close together a streak's throws must be, so a run
// can't be stitched together across hours or days.
const diceStreakWindow = 10 * time.Minute

type diceGame struct {
	emoji string
	name  string
	won   func(value int) bool
	// streak, when set, replaces won: the game is won by throwing the same
	// value this many times in a row within diceStreakWindow.
	streak int
}

// diceGames is also the display order for /dicestats. Value ranges are from
// https://core.telegram.org/bots/api#dice.
var diceGames = []diceGame{
	// Slot value-1 encodes the three reels as base-4 digits, so three of a kind
	// (bar, grapes, lemon, seven) lands on 1, 22, 43 and 64.
	{"🎰", "Слоты", func(v int) bool { return v == 1 || v == 22 || v == 43 || v == 64 }, 0},
	{"🎲", "Кубик", nil, 3},
	{"🎯", "Дартс", func(v int) bool { return v == 6 }, 0},
	{"🏀", "Баскетбол", func(v int) bool { return v >= 4 }, 0},
	{"⚽", "Футбол", func(v int) bool { return v >= 3 }, 0},
	{"🎳", "Боулинг", func(v int) bool { return v == 6 }, 0},
}

func findDiceGame(emoji string) (diceGame, bool) {
	emoji = strings.TrimSuffix(emoji, "️")
	for _, g := range diceGames {
		if g.emoji == emoji {
			return g, true
		}
	}
	return diceGame{}, false
}

// isOwnDiceRoll skips forwarded dice: a forward keeps the original value, so
// forwarding someone's jackpot would otherwise count as a win.
func isOwnDiceRoll(update *models.Update) bool {
	return update.Message != nil && update.Message.Dice != nil && update.Message.From != nil && update.Message.ForwardOrigin == nil
}

type diceTally struct {
	userId int64
	throws int
	wins   int
}

// scoreDice turns throws, in the order they were sent, into per-game tallies.
// After a streak win the run starts over, so six equal throws are two wins.
func scoreDice(throws []storage.DiceThrow) map[string][]diceTally {
	type key struct {
		userId int64
		emoji  string
	}

	tallies := map[key]*diceTally{}
	var order []key
	// runs holds each player's current run of equal values, oldest first.
	runs := map[key][]storage.DiceThrow{}

	for _, th := range throws {
		game, ok := findDiceGame(th.Emoji)
		if !ok {
			continue
		}
		k := key{th.UserId, game.emoji}
		t, ok := tallies[k]
		if !ok {
			t = &diceTally{userId: th.UserId}
			tallies[k] = t
			order = append(order, k)
		}
		t.throws++

		if game.streak == 0 {
			if game.won(th.Value) {
				t.wins++
			}
			continue
		}

		run := runs[k]
		if len(run) > 0 && run[0].Value != th.Value {
			run = nil
		}
		run = append(run, th)
		for th.SentAt.Sub(run[0].SentAt) >= diceStreakWindow {
			run = run[1:]
		}
		if len(run) == game.streak {
			t.wins++
			run = nil
		}
		runs[k] = run
	}

	byEmoji := map[string][]diceTally{}
	for _, k := range order {
		byEmoji[k.emoji] = append(byEmoji[k.emoji], *tallies[k])
	}
	for _, ts := range byEmoji {
		sort.SliceStable(ts, func(i, j int) bool {
			if ts[i].wins != ts[j].wins {
				return ts[i].wins > ts[j].wins
			}
			return ts[i].throws < ts[j].throws
		})
	}
	return byEmoji
}

func handleDice(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		userId := update.Message.From.ID
		chatId := update.Message.Chat.ID

		game, ok := findDiceGame(update.Message.Dice.Emoji)
		if !ok {
			return
		}

		err := storage.AddDiceThrow(db, chatId, storage.DiceThrow{
			MessageId: update.Message.ID,
			UserId:    userId,
			Emoji:     game.emoji,
			Value:     update.Message.Dice.Value,
			SentAt:    time.Unix(int64(update.Message.Date), 0),
		})
		if err != nil {
			log.Error("DB failed to add dice throw", "user_id", userId, "chat_id", chatId, "err", err.Error())
		}
	}
}

const diceNameWidth = 18

// fitName pads or cuts a name to a fixed width so the <pre> table lines up.
func fitName(name string) string {
	if utf8.RuneCountInString(name) > diceNameWidth {
		return string([]rune(name)[:diceNameWidth-1]) + "…"
	}
	return name + strings.Repeat(" ", diceNameWidth-utf8.RuneCountInString(name))
}

func handleDiceStats(db *sql.DB, log *slog.Logger) func(context.Context, *bot.Bot, *models.Update) {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chatId := update.Message.Chat.ID

		throws, err := storage.GetDiceThrows(db, chatId)
		if err != nil {
			log.Error("DB failed to get dice throws in chat", "chat_id", chatId, "err", err.Error())
			sendError(ctx, b, log, chatId)
			return
		}
		byEmoji := scoreDice(throws)
		if len(byEmoji) == 0 {
			sendMessage(ctx, b, log, chatId, "Никто ещё не играл(")
			return
		}

		var sections []string
		for _, game := range diceGames {
			tallies := byEmoji[game.emoji]
			if len(tallies) == 0 {
				continue
			}

			var table strings.Builder
			// A 🎲 win takes several throws, so "played" would overstate its games.
			played := "Сыграл"
			if game.streak > 0 {
				played = "Броски"
			}
			fmt.Fprintf(&table, "%s %6s %5s %4s", fitName("Игрок"), played, "Побед", "%")
			for _, t := range tallies {
				firstName, lastName := resolveName(ctx, b, db, log, t.userId, chatId)
				name := firstName
				if lastName != "" {
					name = firstName + " " + lastName
				}
				percent := t.wins * 100 / t.throws
				if game.streak > 0 {
					// Per attempt of `streak` throws, so a perfect run reads 100%
					// like the other games instead of topping out at 33%.
					percent = t.wins * game.streak * 100 / t.throws
				}
				fmt.Fprintf(&table, "\n%s %6d %5d %3d%%", fitName(name), t.throws, t.wins, percent)
			}

			sections = append(sections, fmt.Sprintf("<b>%s %s</b>\n<pre>%s</pre>", game.emoji, game.name, html.EscapeString(table.String())))
		}

		_, err = b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    chatId,
			Text:      strings.Join(sections, "\n"),
			ParseMode: models.ParseModeHTML,
		})
		if err != nil {
			log.Error("Failed to send message to chat", "chat_id", chatId, "err", err.Error())
		}
	}
}
