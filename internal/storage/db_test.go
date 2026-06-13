package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestDB opens a fresh, isolated SQLite database backed by a temp file and
// creates the schema. The file is removed automatically when the test ends.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := createTables(db); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return db
}

func TestAddUserAndExists(t *testing.T) {
	db := newTestDB(t)
	const userID, chatID = int64(100), int64(200)

	exists, err := UserExists(db, userID, chatID)
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if exists {
		t.Fatal("user should not exist before being added")
	}

	if err := AddUser(db, userID, chatID, "Test", ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	exists, err = UserExists(db, userID, chatID)
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if !exists {
		t.Fatal("user should exist after being added")
	}

	// A user added in one chat must not appear to exist in another chat.
	exists, err = UserExists(db, userID, chatID+1)
	if err != nil {
		t.Fatalf("UserExists other chat: %v", err)
	}
	if exists {
		t.Fatal("user should not exist in a different chat")
	}
}

func TestActivateDeactivate(t *testing.T) {
	db := newTestDB(t)
	const userID, chatID = int64(1), int64(2)

	if err := AddUser(db, userID, chatID, "Test", ""); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	// Newly added users default to active.
	active, err := IsActive(db, userID, chatID)
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if !active {
		t.Fatal("new user should be active by default")
	}

	if err := DeactivateUser(db, userID, chatID); err != nil {
		t.Fatalf("DeactivateUser: %v", err)
	}
	active, err = IsActive(db, userID, chatID)
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if active {
		t.Fatal("user should be inactive after DeactivateUser")
	}

	if err := ActivateUser(db, userID, chatID); err != nil {
		t.Fatalf("ActivateUser: %v", err)
	}
	active, err = IsActive(db, userID, chatID)
	if err != nil {
		t.Fatalf("IsActive: %v", err)
	}
	if !active {
		t.Fatal("user should be active after ActivateUser")
	}
}

func TestGetAllPlayersOnlyActiveAndScoped(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(10)

	// Two active players in chatID, one deactivated, and one in another chat.
	mustAddUser(t, db, 1, chatID)
	mustAddUser(t, db, 2, chatID)
	mustAddUser(t, db, 3, chatID)
	mustAddUser(t, db, 4, chatID+1)

	if err := DeactivateUser(db, 3, chatID); err != nil {
		t.Fatalf("DeactivateUser: %v", err)
	}

	players, err := GetAllPlayers(db, chatID)
	if err != nil {
		t.Fatalf("GetAllPlayers: %v", err)
	}

	got := map[int64]bool{}
	for _, p := range players {
		got[p] = true
	}
	if len(players) != 2 || !got[1] || !got[2] {
		t.Fatalf("expected active players {1,2} in chat, got %v", players)
	}
}

func TestGetTodayGameCreatesAndReuses(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(42)

	game, err := GetTodayGame(db, chatID)
	if err != nil {
		t.Fatalf("GetTodayGame (create): %v", err)
	}
	if game.ChatId != chatID {
		t.Fatalf("chat id = %d, want %d", game.ChatId, chatID)
	}
	if game.LoxTelegramId != nil || game.WinTelegramId != nil {
		t.Fatal("fresh game should have no lox and no winner")
	}

	// Calling again the same day must not create a second row.
	if _, err := GetTodayGame(db, chatID); err != nil {
		t.Fatalf("GetTodayGame (reuse): %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM games WHERE chat_id = $1;`, chatID).Scan(&count); err != nil {
		t.Fatalf("count games: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 game row for the day, got %d", count)
	}
}

func TestAddWinnerIsAtomicAndReflectedInGame(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(7)

	mustAddUser(t, db, 111, chatID)
	if _, err := GetTodayGame(db, chatID); err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}

	// First assignment wins.
	set, err := AddWinner(db, 111, chatID)
	if err != nil {
		t.Fatalf("AddWinner: %v", err)
	}
	if !set {
		t.Fatal("first AddWinner should report set=true")
	}

	// Second assignment the same day must be rejected (winner already set).
	mustAddUser(t, db, 222, chatID)
	set, err = AddWinner(db, 222, chatID)
	if err != nil {
		t.Fatalf("AddWinner second: %v", err)
	}
	if set {
		t.Fatal("second AddWinner should report set=false; winner already chosen")
	}

	// The game should still reflect the first winner.
	game, err := GetTodayGame(db, chatID)
	if err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}
	if game.WinTelegramId == nil || *game.WinTelegramId != 111 {
		t.Fatalf("winner telegram id = %v, want 111", game.WinTelegramId)
	}
}

func TestAddLoxIsAtomicAndReflectedInGame(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(8)

	mustAddUser(t, db, 333, chatID)
	if _, err := GetTodayGame(db, chatID); err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}

	set, err := AddLox(db, 333, chatID)
	if err != nil {
		t.Fatalf("AddLox: %v", err)
	}
	if !set {
		t.Fatal("first AddLox should report set=true")
	}

	mustAddUser(t, db, 444, chatID)
	set, err = AddLox(db, 444, chatID)
	if err != nil {
		t.Fatalf("AddLox second: %v", err)
	}
	if set {
		t.Fatal("second AddLox should report set=false; lox already chosen")
	}

	game, err := GetTodayGame(db, chatID)
	if err != nil {
		t.Fatalf("GetTodayGame: %v", err)
	}
	if game.LoxTelegramId == nil || *game.LoxTelegramId != 333 {
		t.Fatalf("lox telegram id = %v, want 333", game.LoxTelegramId)
	}
}

// TestAddWinnerScopedByChat locks in the fix for the bug where the user-table
// lookup ignored chat_id: the same telegram user registered in two chats must
// resolve to the row belonging to the chat the game is in.
func TestAddWinnerScopedByChat(t *testing.T) {
	db := newTestDB(t)
	const userID, chatA, chatB = int64(555), int64(1), int64(2)

	mustAddUser(t, db, userID, chatA)
	mustAddUser(t, db, userID, chatB)
	if _, err := GetTodayGame(db, chatA); err != nil {
		t.Fatalf("GetTodayGame A: %v", err)
	}
	if _, err := GetTodayGame(db, chatB); err != nil {
		t.Fatalf("GetTodayGame B: %v", err)
	}

	set, err := AddWinner(db, userID, chatA)
	if err != nil {
		t.Fatalf("AddWinner: %v", err)
	}
	if !set {
		t.Fatal("AddWinner should report set=true")
	}

	// chatA's game now has a winner; chatB's game must remain untouched.
	gameB, err := GetTodayGame(db, chatB)
	if err != nil {
		t.Fatalf("GetTodayGame B: %v", err)
	}
	if gameB.WinTelegramId != nil {
		t.Fatalf("chat B should have no winner, got %v", *gameB.WinTelegramId)
	}

	// And the winner row stored for chatA must be chatA's user row.
	var winnerChat int64
	err = db.QueryRow(`SELECT u.chat_id FROM games g JOIN users u ON g.winner_id = u.id WHERE g.chat_id = $1;`, chatA).Scan(&winnerChat)
	if err != nil {
		t.Fatalf("lookup winner chat: %v", err)
	}
	if winnerChat != chatA {
		t.Fatalf("winner row belongs to chat %d, want %d", winnerChat, chatA)
	}
}

func TestGetWinnersCountsAndOrdering(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(99)

	mustAddUser(t, db, 10, chatID)
	mustAddUser(t, db, 20, chatID)

	// User 10 wins on three different days, user 20 on one.
	insertGame(t, db, chatID, "2024-01-01", nil, userRowID(t, db, 10, chatID))
	insertGame(t, db, chatID, "2024-01-02", nil, userRowID(t, db, 10, chatID))
	insertGame(t, db, chatID, "2024-01-03", nil, userRowID(t, db, 10, chatID))
	insertGame(t, db, chatID, "2024-01-04", nil, userRowID(t, db, 20, chatID))

	winners, err := GetWinners(db, chatID)
	if err != nil {
		t.Fatalf("GetWinners: %v", err)
	}
	if len(winners) != 2 {
		t.Fatalf("expected 2 winners, got %d (%+v)", len(winners), winners)
	}
	// Ordered by count descending.
	if winners[0].UserId != 10 || winners[0].Lucky != 3 {
		t.Fatalf("top winner = %+v, want user 10 with 3", winners[0])
	}
	if winners[1].UserId != 20 || winners[1].Lucky != 1 {
		t.Fatalf("second winner = %+v, want user 20 with 1", winners[1])
	}
}

func TestGetLoxesCountsAndOrdering(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(77)

	mustAddUser(t, db, 30, chatID)
	mustAddUser(t, db, 40, chatID)

	insertGame(t, db, chatID, "2024-02-01", userRowID(t, db, 40, chatID), nil)
	insertGame(t, db, chatID, "2024-02-02", userRowID(t, db, 40, chatID), nil)
	insertGame(t, db, chatID, "2024-02-03", userRowID(t, db, 30, chatID), nil)

	loxes, err := GetLoxes(db, chatID)
	if err != nil {
		t.Fatalf("GetLoxes: %v", err)
	}
	if len(loxes) != 2 {
		t.Fatalf("expected 2 loxes, got %d (%+v)", len(loxes), loxes)
	}
	if loxes[0].UserId != 40 || loxes[0].Lucky != 2 {
		t.Fatalf("top lox = %+v, want user 40 with 2", loxes[0])
	}
}

func TestGetWinnersEmpty(t *testing.T) {
	db := newTestDB(t)

	winners, err := GetWinners(db, 123)
	if err != nil {
		t.Fatalf("GetWinners: %v", err)
	}
	if len(winners) != 0 {
		t.Fatalf("expected no winners, got %+v", winners)
	}
}

func TestAddUserStoresName(t *testing.T) {
	db := newTestDB(t)
	const userID, chatID = int64(1), int64(2)

	if err := AddUser(db, userID, chatID, "Иван", "Петров"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	fn, ln, ok, err := GetUserNameDB(db, userID, chatID)
	if err != nil {
		t.Fatalf("GetUserNameDB: %v", err)
	}
	if !ok {
		t.Fatal("user should be found")
	}
	if fn != "Иван" || ln != "Петров" {
		t.Fatalf("stored name = %q %q, want Иван Петров", fn, ln)
	}
}

func TestUpdateUserName(t *testing.T) {
	db := newTestDB(t)
	const userID, chatID = int64(1), int64(2)

	if err := AddUser(db, userID, chatID, "Old", "Name"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := UpdateUserName(db, userID, chatID, "New", "Surname"); err != nil {
		t.Fatalf("UpdateUserName: %v", err)
	}

	fn, ln, _, err := GetUserNameDB(db, userID, chatID)
	if err != nil {
		t.Fatalf("GetUserNameDB: %v", err)
	}
	if fn != "New" || ln != "Surname" {
		t.Fatalf("stored name = %q %q, want New Surname", fn, ln)
	}
}

// TestUpdateUserNameDoesNotCreateUser ensures the name-refresh path never
// registers a new user: updating an unknown user is a no-op.
func TestUpdateUserNameDoesNotCreateUser(t *testing.T) {
	db := newTestDB(t)
	const userID, chatID = int64(99), int64(2)

	if err := UpdateUserName(db, userID, chatID, "Ghost", ""); err != nil {
		t.Fatalf("UpdateUserName: %v", err)
	}

	exists, err := UserExists(db, userID, chatID)
	if err != nil {
		t.Fatalf("UserExists: %v", err)
	}
	if exists {
		t.Fatal("UpdateUserName must not create a user row")
	}
}

func TestGetUserNameMissing(t *testing.T) {
	db := newTestDB(t)

	_, _, ok, err := GetUserNameDB(db, 1, 2)
	if err != nil {
		t.Fatalf("GetUserNameDB: %v", err)
	}
	if ok {
		t.Fatal("ok should be false for an unregistered user")
	}
}

// TestNameColumnsMigrateOldSchema verifies that a database created before names
// were stored gains the columns (without losing existing rows) when the schema
// setup runs against it.
func TestNameColumnsMigrateOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Pre-names schema: users table without first_name/last_name.
	_, err = db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		chat_id INTEGER NOT NULL,
		active BOOLEAN NOT NULL DEFAULT TRUE
	);`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (user_id, chat_id) VALUES (7, 8);`); err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}

	// Running schema setup should migrate the table in place.
	if err := createTables(db); err != nil {
		t.Fatalf("createTables (migration): %v", err)
	}

	// The legacy user survives, with empty (not missing) name columns...
	fn, ln, ok, err := GetUserNameDB(db, 7, 8)
	if err != nil {
		t.Fatalf("GetUserNameDB: %v", err)
	}
	if !ok {
		t.Fatal("legacy user should still exist after migration")
	}
	if fn != "" || ln != "" {
		t.Fatalf("legacy name = %q %q, want empty", fn, ln)
	}

	// ...and the new columns are usable.
	if err := UpdateUserName(db, 7, 8, "Backfilled", ""); err != nil {
		t.Fatalf("UpdateUserName after migration: %v", err)
	}
	if fn, _, _, _ := GetUserNameDB(db, 7, 8); fn != "Backfilled" {
		t.Fatalf("name after backfill = %q, want Backfilled", fn)
	}

	// Migration is idempotent: running it again must not error.
	if err := createTables(db); err != nil {
		t.Fatalf("createTables (second run): %v", err)
	}
}

func TestAddFakeUser(t *testing.T) {
	db := newTestDB(t)
	const chatID = int64(100)

	// A real (positive-id) player already in the chat.
	mustAddUser(t, db, 500, chatID)

	id1, err := AddFakeUser(db, chatID, "Вася", "")
	if err != nil {
		t.Fatalf("AddFakeUser: %v", err)
	}
	id2, err := AddFakeUser(db, chatID, "Петя", "")
	if err != nil {
		t.Fatalf("AddFakeUser: %v", err)
	}

	// Fakes get negative ids, below any real id, and distinct from each other.
	if id1 != -1 || id2 != -2 {
		t.Fatalf("fake ids = %d, %d; want -1, -2", id1, id2)
	}

	// The name is stored and the fake is an active player.
	fn, _, ok, err := GetUserNameDB(db, id1, chatID)
	if err != nil || !ok {
		t.Fatalf("GetUserNameDB(fake) ok=%v err=%v", ok, err)
	}
	if fn != "Вася" {
		t.Fatalf("fake name = %q, want Вася", fn)
	}

	players, err := GetAllPlayers(db, chatID)
	if err != nil {
		t.Fatalf("GetAllPlayers: %v", err)
	}
	if len(players) != 3 {
		t.Fatalf("expected 3 active players (1 real + 2 fake), got %d", len(players))
	}
}

func TestAddFakeUserFirstInChat(t *testing.T) {
	db := newTestDB(t)

	id, err := AddFakeUser(db, 100, "Один", "")
	if err != nil {
		t.Fatalf("AddFakeUser: %v", err)
	}
	if id != -1 {
		t.Fatalf("first fake id = %d, want -1", id)
	}
}

// --- helpers ---

func mustAddUser(t *testing.T, db *sql.DB, userID, chatID int64) {
	t.Helper()
	if err := AddUser(db, userID, chatID, fmt.Sprintf("User%d", userID), ""); err != nil {
		t.Fatalf("AddUser(%d, %d): %v", userID, chatID, err)
	}
}

// userRowID returns the users.id primary key for a (telegram user, chat) pair.
func userRowID(t *testing.T, db *sql.DB, userID, chatID int64) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`SELECT id FROM users WHERE user_id = $1 AND chat_id = $2;`, userID, chatID).Scan(&id)
	if err != nil {
		t.Fatalf("userRowID(%d, %d): %v", userID, chatID, err)
	}
	return id
}

// insertGame inserts a game row directly, used to seed historical results for
// stats tests. loxRowID/winnerRowID are users.id values (or nil).
func insertGame(t *testing.T, db *sql.DB, chatID int64, date string, loxRowID, winnerRowID interface{}) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO games (chat_id, date, lox_id, winner_id) VALUES ($1, $2, $3, $4);`,
		chatID, date, loxRowID, winnerRowID,
	)
	if err != nil {
		t.Fatalf("insertGame: %v", err)
	}
}
