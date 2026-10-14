package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// dayOffset shifts "today" for testing; it is zero in production. Games are
// keyed by date, so moving it starts a new day's game.
var dayOffset atomic.Int64

func currentDate() string {
	year, month, day := time.Now().AddDate(0, 0, int(dayOffset.Load())).Date()
	return fmt.Sprintf("%d-%02d-%02d", year, int(month), day)
}

func AdvanceDay() string {
	dayOffset.Add(1)
	return currentDate()
}

func ResetDay() string {
	dayOffset.Store(0)
	return currentDate()
}

type GotLucky struct {
	UserId int64
	Lucky  int64
}

type Game struct {
	ChatId        int64
	Date          string
	LoxTelegramId *int64
	WinTelegramId *int64
}

func CreateConnection() (*sql.DB, error) {
	exec, err := os.Executable()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(exec)

	return Open(filepath.Join(dir, "database", "database.sqlite"))
}

// Open is the path-explicit core of CreateConnection, exposed so tests can
// point at a temporary database.
func Open(path string) (*sql.DB, error) {
	// Handlers run concurrently and database/sql pools connections, so writes
	// can collide; without a busy timeout the loser fails at once with
	// SQLITE_BUSY instead of waiting its turn. It's per connection, hence the DSN.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}

	if err := createTables(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func createTables(db *sql.DB) error {
	query := `
PRAGMA foreign_keys = 1;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL ,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    first_name TEXT,
    last_name TEXT
);

CREATE TABLE IF NOT EXISTS games (
    id INTEGER PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    date TEXT NOT NULL,
    lox_id INTEGER,
    winner_id INTEGER,

	FOREIGN KEY (lox_id) REFERENCES users(id),
	FOREIGN KEY (winner_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS dice_throws (
    chat_id INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    emoji TEXT NOT NULL,
    value INTEGER NOT NULL,
    sent_at INTEGER NOT NULL,

    PRIMARY KEY (chat_id, message_id)
);
`
	if _, err := db.Exec(query); err != nil {
		return err
	}

	if err := ensureUserNameColumns(db); err != nil {
		return err
	}

	return dedupeUsers(db)
}

// dedupeUsers collapses duplicate users rows that share a (user_id, chat_id) —
// older databases had no UNIQUE constraint, so a single person could end up
// with several rows, which made them appear more than once in /stats. It keeps
// the lowest id of each group, repoints games at it, removes the rest, and adds
// the UNIQUE index so duplicates can't recur. Idempotent: a clean database has
// no duplicates and the index already exists.
func dedupeUsers(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Repoint games before deleting so no foreign key dangles. The subquery maps
	// a row to the lowest id sharing its (user_id, chat_id) — its group's keeper.
	if _, err := tx.Exec(`UPDATE games SET winner_id =
		(SELECT MIN(u2.id) FROM users u1 JOIN users u2 ON u2.user_id = u1.user_id AND u2.chat_id = u1.chat_id WHERE u1.id = games.winner_id)
		WHERE winner_id IS NOT NULL;`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE games SET lox_id =
		(SELECT MIN(u2.id) FROM users u1 JOIN users u2 ON u2.user_id = u1.user_id AND u2.chat_id = u1.chat_id WHERE u1.id = games.lox_id)
		WHERE lox_id IS NOT NULL;`); err != nil {
		return err
	}

	keepers := `(SELECT MIN(id) FROM users GROUP BY user_id, chat_id)`

	// Carry forward a duplicate's active flag and any non-empty name so nothing
	// useful is lost when the extra rows are deleted.
	if _, err := tx.Exec(`UPDATE users SET active = TRUE WHERE id IN ` + keepers +
		` AND EXISTS (SELECT 1 FROM users d WHERE d.user_id = users.user_id AND d.chat_id = users.chat_id AND d.active = TRUE);`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET
		first_name = COALESCE(NULLIF(first_name, ''), (SELECT d.first_name FROM users d WHERE d.user_id = users.user_id AND d.chat_id = users.chat_id AND d.first_name IS NOT NULL AND d.first_name != '' ORDER BY d.id LIMIT 1)),
		last_name  = COALESCE(NULLIF(last_name, ''),  (SELECT d.last_name  FROM users d WHERE d.user_id = users.user_id AND d.chat_id = users.chat_id AND d.last_name  IS NOT NULL AND d.last_name  != '' ORDER BY d.id LIMIT 1))
		WHERE id IN ` + keepers + `;`); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM users WHERE id NOT IN ` + keepers + `;`); err != nil {
		return err
	}

	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_user_chat ON users(user_id, chat_id);`); err != nil {
		return err
	}

	return tx.Commit()
}

// ensureUserNameColumns adds the first_name/last_name columns to databases
// created before names were stored. CREATE TABLE IF NOT EXISTS never alters an
// existing table, so older databases need this migration. It is idempotent.
func ensureUserNameColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(users);`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	existing := map[string]bool{}
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, col := range []string{"first_name", "last_name"} {
		if existing[col] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN ` + col + ` TEXT;`); err != nil {
			return err
		}
	}

	return nil
}

func GetTodayGame(db *sql.DB, chatId int64) (Game, error) {
	dateFormated := currentDate()

	statement := `
SELECT games.chat_id, games.date, lox.user_id, winner.user_id FROM games LEFT OUTER JOIN users lox ON lox.id = games.lox_id LEFT OUTER JOIN users winner ON winner.id = games.winner_id WHERE games.chat_id = $1 AND games.date = $2;
`
	row := db.QueryRow(statement, chatId, dateFormated)
	if row.Err() != nil {
		return Game{}, row.Err()
	}

	var game Game
	err := row.Scan(&game.ChatId, &game.Date, &game.LoxTelegramId, &game.WinTelegramId)
	if err == nil {
		return game, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Game{}, err
	}

	statement = `
INSERT INTO games (chat_id, date, lox_id, winner_id) VALUES ($1, $2, null, null);
`
	_, err = db.Exec(statement, chatId, dateFormated)
	if err != nil {
		return Game{}, err
	}

	return Game{chatId, dateFormated, nil, nil}, nil
}

// AddUser inserts a player. ON CONFLICT DO NOTHING makes it a no-op if the
// (user_id, chat_id) already exists, so a registration that loses a race
// against a concurrent one fails silently instead of duplicating the row.
func AddUser(db *sql.DB, userId int64, chatId int64, firstName, lastName string) error {
	statement := `
INSERT INTO users (user_id, chat_id, first_name, last_name)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, chat_id) DO NOTHING;
`
	_, err := db.Exec(statement, userId, chatId, firstName, lastName)

	return err
}

// AddFakeUser registers a synthetic test player. Real Telegram ids are
// positive, so fakes get negative ids (below the chat's current minimum) to
// avoid colliding with real users.
func AddFakeUser(db *sql.DB, chatId int64, firstName, lastName string) (int64, error) {
	var minID sql.NullInt64
	if err := db.QueryRow(`SELECT MIN(user_id) FROM users WHERE chat_id = $1;`, chatId).Scan(&minID); err != nil {
		return 0, err
	}

	newID := int64(-1)
	if minID.Valid && minID.Int64 <= 0 {
		newID = minID.Int64 - 1
	}

	if err := AddUser(db, newID, chatId, firstName, lastName); err != nil {
		return 0, err
	}

	return newID, nil
}

// UpdateUserName updates nothing if the user is not registered in the chat.
func UpdateUserName(db *sql.DB, userId int64, chatId int64, firstName, lastName string) error {
	statement := `
UPDATE users SET first_name = $1, last_name = $2 WHERE user_id = $3 AND chat_id = $4;
`
	_, err := db.Exec(statement, firstName, lastName, userId, chatId)

	return err
}

func GetUserNameDB(db *sql.DB, userId int64, chatId int64) (firstName, lastName string, ok bool, err error) {
	var fn, ln sql.NullString
	row := db.QueryRow(`SELECT first_name, last_name FROM users WHERE user_id = $1 AND chat_id = $2;`, userId, chatId)
	switch scanErr := row.Scan(&fn, &ln); {
	case errors.Is(scanErr, sql.ErrNoRows):
		return "", "", false, nil
	case scanErr != nil:
		return "", "", false, scanErr
	}

	return fn.String, ln.String, true, nil
}

// AddWinner records the winner for today's game in the chat, but only if one has
// not already been set. It returns true when this call is the one that set the
// winner, so concurrent callers don't both announce a pick.
func AddWinner(db *sql.DB, userId int64, chatId int64) (bool, error) {
	dateFormated := currentDate()

	var userTableId int64
	row := db.QueryRow(`SELECT id FROM users WHERE user_id = $1 AND chat_id = $2;`, userId, chatId)
	if err := row.Err(); err != nil {
		return false, err
	}
	if err := row.Scan(&userTableId); err != nil {
		return false, err
	}

	statement := `
UPDATE games SET winner_id = $1 WHERE chat_id = $2 AND date = $3 AND winner_id IS NULL;
`
	res, err := db.Exec(statement, userTableId, chatId, dateFormated)
	if err != nil {
		return false, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected > 0, nil
}

// AddLox records the lox for today's game in the chat, but only if one has not
// already been set. It returns true when this call is the one that set the lox,
// so concurrent callers don't both announce a pick.
func AddLox(db *sql.DB, userId int64, chatId int64) (bool, error) {
	dateFormated := currentDate()

	var userTableId int64
	row := db.QueryRow(`SELECT id FROM users WHERE user_id = $1 AND chat_id = $2;`, userId, chatId)
	if err := row.Err(); err != nil {
		return false, err
	}
	if err := row.Scan(&userTableId); err != nil {
		return false, err
	}

	statement := `
UPDATE games SET lox_id = $1 WHERE chat_id = $2 AND date = $3 AND lox_id IS NULL;
`
	res, err := db.Exec(statement, userTableId, chatId, dateFormated)
	if err != nil {
		return false, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected > 0, nil
}

func GetWinners(db *sql.DB, chatId int64) ([]GotLucky, error) {
	statement := `
SELECT users.user_id, COUNT(users.user_id) as got_lucky FROM games INNER JOIN users ON games.winner_id = users.id WHERE games.chat_id = $1 GROUP BY users.user_id ORDER BY got_lucky DESC;
`
	rows, err := db.Query(statement, chatId)
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var winners []GotLucky
	for rows.Next() {
		var user GotLucky
		if err = rows.Scan(&user.UserId, &user.Lucky); err != nil {
			return nil, err
		}
		winners = append(winners, user)
	}

	return winners, rows.Err()
}

func GetLoxes(db *sql.DB, chatId int64) ([]GotLucky, error) {
	statement := `
SELECT users.user_id, COUNT(users.user_id) as got_unlucky FROM games INNER JOIN users ON games.lox_id = users.id WHERE games.chat_id = $1 GROUP BY users.user_id ORDER BY got_unlucky DESC;
`
	rows, err := db.Query(statement, chatId)
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var loxes []GotLucky
	for rows.Next() {
		var user GotLucky
		if err = rows.Scan(&user.UserId, &user.Lucky); err != nil {
			return nil, err
		}
		loxes = append(loxes, user)
	}

	return loxes, rows.Err()
}

func UserExists(db *sql.DB, userId int64, chatId int64) (bool, error) {
	statement := `
SELECT EXISTS(SELECT 1 FROM users WHERE chat_id = $1 AND user_id = $2);
`
	row := db.QueryRow(statement, chatId, userId)
	if row.Err() != nil {
		return false, row.Err()
	}

	exists := 0
	if err := row.Scan(&exists); err != nil {
		return false, err
	}

	return exists > 0, nil
}

func IsActive(db *sql.DB, userId int64, chatId int64) (bool, error) {
	statement := `
SELECT active FROM users WHERE chat_id = $1 AND user_id = $2;
`
	row := db.QueryRow(statement, chatId, userId)
	if row.Err() != nil {
		return false, row.Err()
	}

	exists := false
	if err := row.Scan(&exists); err != nil {
		return false, err
	}

	return exists, nil
}

func DeactivateUser(db *sql.DB, userId int64, chatId int64) error {
	statement := `
UPDATE users SET active = FALSE WHERE user_id = $1 and chat_id = $2;
`
	_, err := db.Exec(statement, userId, chatId)

	return err
}

func ActivateUser(db *sql.DB, userId int64, chatId int64) error {
	statement := `
UPDATE users SET active = TRUE WHERE user_id = $1 and chat_id = $2;
`
	_, err := db.Exec(statement, userId, chatId)

	return err
}

func GetAllPlayers(db *sql.DB, chatId int64) ([]int64, error) {
	statement := `
SELECT user_id FROM users WHERE chat_id = $1 AND active = TRUE;
`
	rows, err := db.Query(statement, chatId)
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var playerIds []int64
	for rows.Next() {
		var user int64
		if err = rows.Scan(&user); err != nil {
			return nil, err
		}
		playerIds = append(playerIds, user)
	}

	return playerIds, rows.Err()
}

type DiceThrow struct {
	MessageId int
	UserId    int64
	Emoji     string
	Value     int
	SentAt    time.Time
}

// AddDiceThrow stores raw throws rather than running totals: handlers run
// concurrently, so quick throws can arrive out of order, and scoring them later
// by message id is the only way to see them in the order they were sent. The
// key also drops a redelivered update instead of counting it twice.
func AddDiceThrow(db *sql.DB, chatId int64, throw DiceThrow) error {
	statement := `
INSERT INTO dice_throws (chat_id, message_id, user_id, emoji, value, sent_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (chat_id, message_id) DO NOTHING;
`
	_, err := db.Exec(statement, chatId, throw.MessageId, throw.UserId, throw.Emoji, throw.Value, throw.SentAt.Unix())

	return err
}

// GetDiceThrows returns the chat's throws in the order they were sent.
func GetDiceThrows(db *sql.DB, chatId int64) ([]DiceThrow, error) {
	statement := `
SELECT message_id, user_id, emoji, value, sent_at FROM dice_throws WHERE chat_id = $1 ORDER BY message_id;
`
	rows, err := db.Query(statement, chatId)
	if err != nil {
		return nil, err
	}
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var throws []DiceThrow
	for rows.Next() {
		var (
			throw  DiceThrow
			sentAt int64
		)
		if err = rows.Scan(&throw.MessageId, &throw.UserId, &throw.Emoji, &throw.Value, &sentAt); err != nil {
			return nil, err
		}
		throw.SentAt = time.Unix(sentAt, 0)
		throws = append(throws, throw)
	}

	return throws, rows.Err()
}
