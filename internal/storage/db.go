package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

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

// Open opens (or creates) the SQLite database at path and ensures the schema
// exists. It is the path-explicit core of CreateConnection, exposed so tests
// can point at a temporary database.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
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
)
`
	if _, err := db.Exec(query); err != nil {
		return err
	}

	return ensureUserNameColumns(db)
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
	year, month, day := time.Now().Date()
	monthInt := int(month)
	dateFormated := fmt.Sprintf("%d-%02d-%02d", year, monthInt, day)

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

func AddUser(db *sql.DB, userId int64, chatId int64, firstName, lastName string) error {
	statement := `
INSERT INTO users (user_id, chat_id, first_name, last_name)
VALUES ($1, $2, $3, $4);
`
	_, err := db.Exec(statement, userId, chatId, firstName, lastName)

	return err
}

// UpdateUserName refreshes the stored Telegram name for an existing player. It
// updates nothing if the user is not registered in the chat.
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
	year, month, day := time.Now().Date()
	monthInt := int(month)
	dateFormated := fmt.Sprintf("%d-%02d-%02d", year, monthInt, day)

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
	year, month, day := time.Now().Date()
	monthInt := int(month)
	dateFormated := fmt.Sprintf("%d-%02d-%02d", year, monthInt, day)

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
SELECT users.user_id, COUNT(users.user_id) as got_lucky FROM games INNER JOIN users ON games.winner_id = users.id WHERE games.chat_id = $1 GROUP BY games.winner_id ORDER BY got_lucky DESC;
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
SELECT users.user_id, COUNT(users.user_id) as got_unlucky FROM games INNER JOIN users ON games.lox_id = users.id WHERE games.chat_id = $1 GROUP BY games.lox_id ORDER BY got_unlucky DESC;
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
