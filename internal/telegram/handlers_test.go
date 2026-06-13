package telegram

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"desolidify.ru/lox-bot/internal/storage"
)

// TestMain makes the suspense pauses instant so handler tests don't sleep.
func TestMain(m *testing.M) {
	waitTime = 0
	os.Exit(m.Run())
}

// sentMessage is a sendMessage call captured by the fake Telegram API.
type sentMessage struct {
	chatID int64
	text   string
}

// fakeAPI impersonates the Telegram Bot API. It records sendMessage calls and
// answers getChatMember with a synthetic user derived from the requested id.
type fakeAPI struct {
	mu       sync.Mutex
	messages []sentMessage
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	method := path.Base(r.URL.Path) // .../bot<token>/<method>
	// The library sends params as multipart/form-data, one field per param.
	_ = r.ParseMultipartForm(1 << 20)

	switch method {
	case "sendMessage":
		chatID, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
		f.mu.Lock()
		f.messages = append(f.messages, sentMessage{chatID: chatID, text: r.FormValue("text")})
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)

	case "getChatMember":
		userID, _ := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
		// Name the user deterministically so tests can assert on it.
		_, _ = fmt.Fprintf(w,
			`{"ok":true,"result":{"status":"member","user":{"id":%d,"first_name":"User%d"}}}`,
			userID, userID)

	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}
}

// lastMessage returns the most recently captured sendMessage call.
func (f *fakeAPI) lastMessage(t *testing.T) sentMessage {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) == 0 {
		t.Fatal("no messages were sent")
	}
	return f.messages[len(f.messages)-1]
}

// newBot wires a bot to the fake API and returns both.
func newBot(t *testing.T) (*bot.Bot, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(api.handler))
	t.Cleanup(srv.Close)

	b, err := bot.New("test", bot.WithSkipGetMe(), bot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	return b, api
}

// testDB opens a temp-file database with the real schema.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// msg builds an update for a text command from a user in a chat.
func msg(userID, chatID int64) *models.Update {
	return &models.Update{
		Message: &models.Message{
			ID:   10,
			From: &models.User{ID: userID, FirstName: fmt.Sprintf("User%d", userID)},
			Chat: models.Chat{ID: chatID, Type: models.ChatTypeGroup},
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHandlerStartSendsWelcome(t *testing.T) {
	b, api := newBot(t)
	log := discardLogger()

	handlerStart(log)(context.Background(), b, msg(1, 555))

	got := api.lastMessage(t)
	if got.chatID != 555 {
		t.Fatalf("chat id = %d, want 555", got.chatID)
	}
	if !strings.Contains(got.text, "Лох дня") {
		t.Fatalf("welcome text missing expected content: %q", got.text)
	}
}

func TestHandlerRegisterNewUser(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(42), int64(100)

	handlerRegister(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	// The user is now persisted...
	exists, err := storage.UserExists(db, userID, chatID)
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if !exists {
		t.Fatal("user should have been added to the database")
	}
	// ...and was welcomed.
	if got := api.lastMessage(t); !strings.Contains(got.text, "в игре") {
		t.Fatalf("expected join confirmation, got %q", got.text)
	}
}

func TestHandlerRegisterAlreadyActive(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(42), int64(100)

	if err := storage.AddUser(db, userID, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handlerRegister(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	if got := api.lastMessage(t); !strings.Contains(got.text, "уже в игре") {
		t.Fatalf("expected already-in-game reply, got %q", got.text)
	}
}

func TestHandleUnregisterNotParticipating(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)

	handleUnregister(db, discardLogger())(context.Background(), b, msg(7, 100))

	if got := api.lastMessage(t); !strings.Contains(got.text, "не участвуешь") {
		t.Fatalf("expected not-participating reply, got %q", got.text)
	}
}

func TestHandleUnregisterDeactivates(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(7), int64(100)

	if err := storage.AddUser(db, userID, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handleUnregister(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	active, err := storage.IsActive(db, userID, chatID)
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if active {
		t.Fatal("user should be inactive after /leave")
	}
	if got := api.lastMessage(t); !strings.Contains(got.text, "больше не в игре") {
		t.Fatalf("expected leave confirmation, got %q", got.text)
	}
}

func TestHandleRunNoPlayers(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)

	handleRun(db, discardLogger())(context.Background(), b, msg(1, 100))

	if got := api.lastMessage(t); !strings.Contains(got.text, "Нет активных игроков") {
		t.Fatalf("expected no-players reply, got %q", got.text)
	}
}

func TestHandleRunPicksAndRecordsWinner(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(77), int64(100)

	if err := storage.AddUser(db, userID, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	handleRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	// The winner is recorded for today's game.
	game, err := storage.GetTodayGame(db, chatID)
	if err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}
	if game.WinTelegramId == nil || *game.WinTelegramId != userID {
		t.Fatalf("winner = %v, want %d", game.WinTelegramId, userID)
	}

	// The final (dramatic) message announces the chosen player by name.
	last := api.lastMessage(t)
	if !strings.Contains(last.text, "Красавчик дня") || !strings.Contains(last.text, "User77") {
		t.Fatalf("final message missing winner announcement: %q", last.text)
	}
}

func TestHandleRunAnnouncesExistingWinner(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(77), int64(100)

	if err := storage.AddUser(db, userID, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	// First run picks the winner.
	handleRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))
	firstWinner := *mustGame(t, db, chatID).WinTelegramId

	// Second run the same day should re-announce the same winner, not re-pick.
	handleRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	if got := *mustGame(t, db, chatID).WinTelegramId; got != firstWinner {
		t.Fatalf("winner changed on second run: %d -> %d", firstWinner, got)
	}
	if got := api.lastMessage(t); !strings.Contains(got.text, "Красавчик Дня") {
		t.Fatalf("expected existing-winner announcement, got %q", got.text)
	}
}

// TestHandleRunShowsResultAfterAllLeft is a regression test: once a winner is
// decided, /run must still announce it even after every player has left the
// game (previously it replied "no active players").
func TestHandleRunShowsResultAfterAllLeft(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(77), int64(100)

	if err := storage.AddUser(db, userID, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	// Decide the winner, then the only player leaves.
	handleRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))
	handleUnregister(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	handleRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	if got := api.lastMessage(t); !strings.Contains(got.text, "Красавчик Дня") {
		t.Fatalf("expected existing winner to be announced, got %q", got.text)
	}
}

// TestHandleLoxRunShowsResultAfterAllLeft is the same regression for /lox.
func TestHandleLoxRunShowsResultAfterAllLeft(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const userID, chatID = int64(88), int64(100)

	if err := storage.AddUser(db, userID, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	handleLoxRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))
	handleUnregister(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	handleLoxRun(db, discardLogger())(context.Background(), b, msg(userID, chatID))

	if got := api.lastMessage(t); !strings.Contains(got.text, "Лох Дня") {
		t.Fatalf("expected existing lox to be announced, got %q", got.text)
	}
}

func TestHandleStatsListsWinners(t *testing.T) {
	b, api := newBot(t)
	db := testDB(t)
	const chatID = int64(100)

	if err := storage.AddUser(db, 5, chatID); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if _, err := storage.GetTodayGame(db, chatID); err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}
	if _, err := storage.AddWinner(db, 5, chatID); err != nil {
		t.Fatalf("AddWinner: %v", err)
	}

	handleStats(db, discardLogger())(context.Background(), b, msg(5, chatID))

	got := api.lastMessage(t)
	if !strings.Contains(got.text, "Был Красавчиком Дня") {
		t.Fatalf("stats header missing: %q", got.text)
	}
	if !strings.Contains(got.text, "User5") || !strings.Contains(got.text, "1 раз") {
		t.Fatalf("stats body missing winner/count: %q", got.text)
	}
}

func mustGame(t *testing.T, db *sql.DB, chatID int64) storage.Game {
	t.Helper()
	g, err := storage.GetTodayGame(db, chatID)
	if err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}
	return g
}
