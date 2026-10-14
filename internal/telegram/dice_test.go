package telegram

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"desolidify.ru/lox-bot/internal/storage"
)

var diceEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func diceMsg(userID, chatID int64, emoji string, value int) *models.Update {
	u := msg(userID, chatID)
	u.Message.Dice = &models.Dice{Emoji: emoji, Value: value}
	return u
}

// throwsAt builds one user's 🎲 throws, all `gap` apart, starting at start.
func throwsAt(userID int64, start time.Time, gap time.Duration, values ...int) []storage.DiceThrow {
	var ts []storage.DiceThrow
	for i, v := range values {
		ts = append(ts, storage.DiceThrow{UserId: userID, Emoji: "🎲", Value: v, SentAt: start.Add(time.Duration(i) * gap)})
	}
	return ts
}

// numbered assigns message ids in slice order, as Telegram would.
func numbered(ts []storage.DiceThrow) []storage.DiceThrow {
	for i := range ts {
		ts[i].MessageId = i + 1
	}
	return ts
}

func tallyOf(byEmoji map[string][]diceTally, emoji string, userID int64) (throws, wins int) {
	for _, t := range byEmoji[emoji] {
		if t.userId == userID {
			return t.throws, t.wins
		}
	}
	return 0, 0
}

func TestDiceWinRules(t *testing.T) {
	cases := []struct {
		emoji string
		value int
		won   bool
	}{
		{"🎰", 1, true}, {"🎰", 22, true}, {"🎰", 43, true}, {"🎰", 64, true},
		{"🎰", 2, false}, {"🎰", 21, false}, {"🎰", 63, false},
		{"🎯", 6, true}, {"🎯", 5, false}, {"🎯", 1, false},
		{"🏀", 4, true}, {"🏀", 5, true}, {"🏀", 3, false}, {"🏀", 1, false},
		{"⚽", 3, true}, {"⚽", 4, true}, {"⚽", 5, true}, {"⚽", 2, false}, {"⚽", 1, false},
		{"⚽️", 5, true},
		{"🎳", 6, true}, {"🎳", 5, false}, {"🎳", 1, false},
	}
	for _, c := range cases {
		g, ok := findDiceGame(c.emoji)
		if !ok {
			t.Fatalf("unknown emoji %q", c.emoji)
		}
		if got := g.won(c.value); got != c.won {
			t.Errorf("%s %d: won = %v, want %v", c.emoji, c.value, got, c.won)
		}
	}
	if _, ok := findDiceGame("🃏"); ok {
		t.Error("unsupported emoji should not be a game")
	}
}

// Every 🎰 slot value whose three reels match must win, and nothing else.
func TestSlotWinsExactlyOnThreeOfAKind(t *testing.T) {
	g, _ := findDiceGame("🎰")
	for v := 1; v <= 64; v++ {
		left, mid, right := (v-1)%4, (v-1)/4%4, (v-1)/16
		want := left == mid && mid == right
		if got := g.won(v); got != want {
			t.Errorf("slot value %d (reels %d %d %d): won = %v, want %v", v, left, mid, right, got, want)
		}
	}
}

func TestScoreDiceStreakScenarios(t *testing.T) {
	const u = int64(1)
	cases := []struct {
		name   string
		throws []storage.DiceThrow
		wins   int
	}{
		{"three in a row", throwsAt(u, diceEpoch, time.Second, 3, 3, 3), 1},
		{"mismatch", throwsAt(u, diceEpoch, time.Second, 3, 4), 0},
		{"clicks a third after a mismatch", throwsAt(u, diceEpoch, time.Second, 3, 4, 3), 0},
		{"run in the middle of nine", throwsAt(u, diceEpoch, time.Second, 1, 5, 5, 5, 2, 6, 1, 3, 4), 1},
		{"run at the very end", throwsAt(u, diceEpoch, time.Second, 1, 2, 3, 4, 6, 6, 6), 1},
		{"only pairs", throwsAt(u, diceEpoch, time.Second, 1, 2, 2, 3, 3, 1, 1), 0},
		{"same value split by another", throwsAt(u, diceEpoch, time.Second, 4, 4, 1, 4), 0},
		{"four in a row is one win", throwsAt(u, diceEpoch, time.Second, 5, 5, 5, 5), 1},
		{"six in a row is two wins", throwsAt(u, diceEpoch, time.Second, 5, 5, 5, 5, 5, 5), 2},
		{"seven in a row is two wins", throwsAt(u, diceEpoch, time.Second, 5, 5, 5, 5, 5, 5, 5), 2},
		{"back to back runs", throwsAt(u, diceEpoch, time.Second, 2, 2, 2, 3, 3, 3), 2},
		{"single throw", throwsAt(u, diceEpoch, time.Second, 6), 0},

		{"slow throws within the window", throwsAt(u, diceEpoch, 4*time.Minute, 2, 2, 2), 1},
		// 0, 4m59s, 9m58s: the whole run fits in the window.
		{"run just inside the window", throwsAt(u, diceEpoch, diceStreakWindow/2-time.Second, 2, 2, 2), 1},
		// 0, 5m, 10m: first and last are a full window apart.
		{"run exactly a window long", throwsAt(u, diceEpoch, diceStreakWindow/2, 2, 2, 2), 0},
		{"far apart", throwsAt(u, diceEpoch, 11*time.Minute, 1, 1, 1), 0},
		{"crosses midnight", throwsAt(u, time.Date(2026, 3, 1, 23, 58, 0, 0, time.UTC), time.Minute, 2, 4, 4, 4), 1},
		{"next day doesn't continue a run", append(
			throwsAt(u, diceEpoch, time.Second, 6, 6),
			throwsAt(u, diceEpoch.Add(24*time.Hour), time.Second, 6)...), 0},
		// 0, 9m, 12m, 13m: the stale first throw drops out, the last three still count.
		{"stale start drops out", append(
			throwsAt(u, diceEpoch, 9*time.Minute, 4, 4),
			throwsAt(u, diceEpoch.Add(12*time.Minute), time.Minute, 4, 4)...), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			throws, wins := tallyOf(scoreDice(numbered(c.throws)), "🎲", u)
			if throws != len(c.throws) || wins != c.wins {
				t.Fatalf("got %d throws %d wins, want %d throws %d wins", throws, wins, len(c.throws), c.wins)
			}
		})
	}
}

// The example from the spec: A throws twice, B starts, A throws their last,
// B carries on. Each player's run only looks at their own throws.
func TestScoreDiceInterleavedPlayers(t *testing.T) {
	const a, b = int64(1), int64(2)
	at := func(s int) time.Time { return diceEpoch.Add(time.Duration(s) * time.Second) }
	throws := numbered([]storage.DiceThrow{
		{UserId: a, Emoji: "🎲", Value: 3, SentAt: at(0)},
		{UserId: a, Emoji: "🎲", Value: 3, SentAt: at(1)},
		{UserId: b, Emoji: "🎲", Value: 1, SentAt: at(2)},
		{UserId: a, Emoji: "🎲", Value: 3, SentAt: at(3)},
		{UserId: b, Emoji: "🎲", Value: 1, SentAt: at(4)},
		{UserId: b, Emoji: "🎰", Value: 5, SentAt: at(5)}, // other games don't break a run
		{UserId: b, Emoji: "🎲", Value: 1, SentAt: at(6)},
		{UserId: a, Emoji: "🎲", Value: 2, SentAt: at(7)},
	})

	got := scoreDice(throws)
	if n, w := tallyOf(got, "🎲", a); n != 4 || w != 1 {
		t.Fatalf("A: %d throws %d wins, want 4/1", n, w)
	}
	if n, w := tallyOf(got, "🎲", b); n != 3 || w != 1 {
		t.Fatalf("B: %d throws %d wins, want 3/1", n, w)
	}
	if n, w := tallyOf(got, "🎰", b); n != 1 || w != 0 {
		t.Fatalf("B slots: %d throws %d wins, want 1/0", n, w)
	}
}

func TestScoreDiceSingleThrowGames(t *testing.T) {
	const u = int64(1)
	throws := numbered([]storage.DiceThrow{
		{UserId: u, Emoji: "🏀", Value: 5, SentAt: diceEpoch},
		{UserId: u, Emoji: "🏀", Value: 1, SentAt: diceEpoch},
		{UserId: u, Emoji: "🏀", Value: 2, SentAt: diceEpoch},
		{UserId: u, Emoji: "🎰", Value: 64, SentAt: diceEpoch},
		{UserId: u, Emoji: "⚽\uFE0F", Value: 3, SentAt: diceEpoch},
		{UserId: u, Emoji: "🃏", Value: 1, SentAt: diceEpoch},
	})
	got := scoreDice(throws)
	for _, c := range []struct {
		emoji        string
		throws, wins int
	}{{"🏀", 3, 1}, {"🎰", 1, 1}, {"⚽", 1, 1}} {
		if n, w := tallyOf(got, c.emoji, u); n != c.throws || w != c.wins {
			t.Errorf("%s: %d throws %d wins, want %d/%d", c.emoji, n, w, c.throws, c.wins)
		}
	}
	if _, ok := got["🃏"]; ok {
		t.Error("unsupported emoji should not be scored")
	}
}

func TestScoreDiceOrdersByWins(t *testing.T) {
	var throws []storage.DiceThrow
	add := func(userID int64, values ...int) {
		for _, v := range values {
			throws = append(throws, storage.DiceThrow{UserId: userID, Emoji: "🎯", Value: v, SentAt: diceEpoch})
		}
	}
	add(1, 6, 1, 1, 1)
	add(2, 6, 6, 1)
	add(3, 6, 1)
	got := scoreDice(numbered(throws))["🎯"]
	var order []int64
	for _, t := range got {
		order = append(order, t.userId)
	}
	if fmt.Sprint(order) != "[2 3 1]" {
		t.Fatalf("order = %v, want [2 3 1] (wins desc, then fewer throws)", order)
	}
}

// referenceDiceWins is a deliberately different, brute-force implementation
// of the 🎲 rule: scan each player's throws for the earliest-ending three equal
// neighbours that fit in the window, never reusing a throw.
func referenceDiceWins(throws []storage.DiceThrow) map[int64]int {
	perUser := map[int64][]storage.DiceThrow{}
	for _, th := range throws {
		perUser[th.UserId] = append(perUser[th.UserId], th)
	}
	wins := map[int64]int{}
	for user, ts := range perUser {
		used := -1 // index of the last throw that completed a win
		for j := 2; j < len(ts); j++ {
			i := j - 2
			if i <= used {
				continue
			}
			if ts[i].Value == ts[j].Value && ts[i+1].Value == ts[j].Value && ts[j].SentAt.Sub(ts[i].SentAt) < diceStreakWindow {
				wins[user]++
				used = j
			}
		}
	}
	return wins
}

func TestScoreDiceMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 5000; iter++ {
		players := 1 + rng.IntN(3)
		faces := 1 + rng.IntN(3) // few faces so streaks actually happen
		at := diceEpoch
		var throws []storage.DiceThrow
		for i, n := 0, rng.IntN(40); i < n; i++ {
			// Mostly quick clicks, sometimes pauses near or past the window.
			switch rng.IntN(10) {
			case 0:
				at = at.Add(time.Duration(rng.IntN(30)) * time.Minute)
			case 1:
				at = at.Add(diceStreakWindow / 2)
			default:
				at = at.Add(time.Duration(rng.IntN(90)) * time.Second)
			}
			throws = append(throws, storage.DiceThrow{
				UserId: int64(1 + rng.IntN(players)),
				Emoji:  "🎲",
				Value:  1 + rng.IntN(faces),
				SentAt: at,
			})
		}
		numbered(throws)

		perUser := map[int64]int{}
		for _, th := range throws {
			perUser[th.UserId]++
		}
		want := referenceDiceWins(throws)
		for _, tl := range scoreDice(throws)["🎲"] {
			if tl.wins != want[tl.userId] || tl.throws != perUser[tl.userId] {
				var seq []string
				for _, th := range throws {
					seq = append(seq, fmt.Sprintf("u%d:%d@%s", th.UserId, th.Value, th.SentAt.Sub(diceEpoch)))
				}
				t.Fatalf("iteration %d, user %d: got %d throws %d wins, want %d/%d\nthrows: %s",
					iter, tl.userId, tl.throws, tl.wins, perUser[tl.userId], want[tl.userId], strings.Join(seq, " "))
			}
		}
	}
}

func TestIsOwnDiceRoll(t *testing.T) {
	u := diceMsg(5, 100, "🎰", 64)
	if !isOwnDiceRoll(u) {
		t.Fatal("own roll should match")
	}
	u.Message.ForwardOrigin = &models.MessageOrigin{}
	if isOwnDiceRoll(u) {
		t.Fatal("forwarded roll should not match")
	}
	if isOwnDiceRoll(msg(5, 100)) {
		t.Fatal("plain message should not match")
	}
	if isOwnDiceRoll(&models.Update{}) {
		t.Fatal("update without a message should not match")
	}
}

// Handlers run in their own goroutines, so quick throws reach the bot out of
// order. Scoring must still follow the order they were sent in.
func TestHandleDiceOutOfOrderAndConcurrent(t *testing.T) {
	b, _ := newBot(t)
	db := testDB(t)
	const chatID, user = int64(100), int64(5)

	// Sent order: 5,5,5 (win) then 5,3,5 (no win). Processed in the wrong
	// order, the second half could look like 5,5,3 and the first like 5,5,... .
	sent := []struct {
		id, value int
		at        time.Time
	}{
		{1, 5, diceEpoch}, {2, 5, diceEpoch.Add(time.Second)}, {3, 5, diceEpoch.Add(2 * time.Second)},
		{4, 5, diceEpoch.Add(time.Hour)}, {5, 3, diceEpoch.Add(time.Hour + time.Second)}, {6, 5, diceEpoch.Add(time.Hour + 2*time.Second)},
	}

	handle := handleDice(db, discardLogger())
	var wg sync.WaitGroup
	for _, i := range []int{5, 2, 0, 4, 1, 3, 2} { // shuffled, with message 3 delivered twice
		s := sent[i]
		u := diceMsg(user, chatID, "🎲", s.value)
		u.Message.ID = s.id
		u.Message.Date = int(s.at.Unix())
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle(context.Background(), b, u)
		}()
	}
	wg.Wait()

	throws, err := storage.GetDiceThrows(db, chatID)
	if err != nil {
		t.Fatalf("GetDiceThrows: %v", err)
	}
	if len(throws) != len(sent) {
		t.Fatalf("stored %d throws, want %d", len(throws), len(sent))
	}
	if n, w := tallyOf(scoreDice(throws), "🎲", user); n != 6 || w != 1 {
		t.Fatalf("got %d throws %d wins, want 6 throws 1 win", n, w)
	}
}

func TestHandleDiceIgnoresUnknownEmoji(t *testing.T) {
	b, _ := newBot(t)
	db := testDB(t)

	handleDice(db, discardLogger())(context.Background(), b, diceMsg(5, 100, "🃏", 1))

	throws, err := storage.GetDiceThrows(db, 100)
	if err != nil {
		t.Fatalf("GetDiceThrows: %v", err)
	}
	if len(throws) != 0 {
		t.Fatalf("stored %+v, want nothing", throws)
	}
}

func TestHandleDiceStatsTable(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const chatID = int64(100)

	if err := storage.AddUser(db, 5, chatID, "Вася", ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := storage.AddUser(db, 6, chatID, "Константинопольский", "<b>"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handle := handleDice(db, discardLogger())
	id := 0
	throw := func(user int64, emoji string, value int, at time.Time) {
		id++
		u := diceMsg(user, chatID, emoji, value)
		u.Message.ID = id
		u.Message.Date = int(at.Unix())
		handle(context.Background(), b, u)
	}
	old := time.Now().Add(-time.Hour)
	throw(5, "🎲", 2, old)
	throw(5, "🎲", 2, old)
	throw(5, "🎲", 2, old)
	throw(5, "🎲", 1, old.Add(20*time.Minute))
	throw(5, "🎲", 4, old.Add(20*time.Minute))
	throw(5, "🎲", 3, time.Now())
	throw(6, "🎲", 6, old)
	throw(6, "🏀", 5, old)
	throw(6, "🏀", 1, old)
	throw(6, "🏀", 2, old)
	throw(5, "🎰", 1, time.Now())

	handleDiceStats(db, discardLogger())(context.Background(), b, msg(5, chatID))

	got := api.lastMessage(t)
	if got.parseMode != "HTML" {
		t.Fatalf("parse mode = %q, want HTML", got.parseMode)
	}
	want := "<b>🎰 Слоты</b>\n<pre>" +
		"Игрок              Сыграл Побед    %\n" +
		"Вася                    1     1 100%</pre>\n" +
		"<b>🎲 Кубик</b>\n<pre>" +
		"Игрок              Броски Побед    %\n" +
		"Вася                    6     1  50%\n" +
		"Константинопольск…      1     0   0%</pre>\n" +
		"<b>🏀 Баскетбол</b>\n<pre>" +
		"Игрок              Сыграл Побед    %\n" +
		"Константинопольск…      3     1  33%</pre>"
	if got.text != want {
		t.Fatalf("stats message:\n%s\nwant:\n%s", got.text, want)
	}
}

func TestHandleDiceStatsEscapesNames(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const chatID = int64(100)

	if err := storage.AddUser(db, 5, chatID, "<i>&", ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	handleDice(db, discardLogger())(context.Background(), b, diceMsg(5, chatID, "🎯", 6))
	handleDiceStats(db, discardLogger())(context.Background(), b, msg(5, chatID))

	if got := api.lastMessage(t).text; !strings.Contains(got, "&lt;i&gt;&amp;") || strings.Contains(got, "<i>") {
		t.Fatalf("name not escaped: %q", got)
	}
}

func TestHandleDiceStatsEmpty(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)

	handleDiceStats(db, discardLogger())(context.Background(), b, msg(5, 100))

	if got := api.lastMessage(t).text; got != "Никто ещё не играл(" {
		t.Fatalf("unexpected empty stats message: %q", got)
	}
}

func TestHandleDiceStatsPerfectStreakIs100Percent(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const chatID = int64(100)

	if err := storage.AddUser(db, 5, chatID, "Вася", ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	handle := handleDice(db, discardLogger())
	for i := 1; i <= 6; i++ {
		u := diceMsg(5, chatID, "🎲", 5)
		u.Message.ID = i
		u.Message.Date = int(time.Now().Unix())
		handle(context.Background(), b, u)
	}
	handleDiceStats(db, discardLogger())(context.Background(), b, msg(5, chatID))

	if got := api.lastMessage(t).text; !strings.Contains(got, "Вася                    6     2 100%") {
		t.Fatalf("six equal throws should read 2 wins at 100%%: %q", got)
	}
}
