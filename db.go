package main

import (
	"database/sql"
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

	db, err := sql.Open("sqlite", filepath.Join(dir, "database", "database.sqlite"))
	if err != nil {
		return nil, err
	}

	err = createTables(db)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, err
}

func createTables(db *sql.DB) error {
	query := `
PRAGMA foreign_keys = 1;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    chat_id INTEGER NOT NULL ,
    active BOOLEAN NOT NULL DEFAULT TRUE
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
	_, err := db.Exec(query)

	return err
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

	statement = `
INSERT INTO games (chat_id, date, lox_id, winner_id) VALUES ($1, $2, null, null);
`
	_, err = db.Exec(statement, chatId, dateFormated, game.LoxTelegramId)
	if err != nil {
		return Game{}, err
	}

	return Game{chatId, dateFormated, nil, nil}, nil
}

func AddUser(db *sql.DB, userId int64, chatId int64) error {
	statement := `
INSERT INTO users (user_id, chat_id)
VALUES ($1, $2);
`
	_, err := db.Exec(statement, userId, chatId)

	return err
}

func AddWinner(db *sql.DB, userId int64, chatId int64) error {
	year, month, day := time.Now().Date()
	monthInt := int(month)
	dateFormated := fmt.Sprintf("%d-%02d-%02d", year, monthInt, day)

	var userTableId int64
	row := db.QueryRow(`SELECT id FROM users WHERE user_id = $1;`, userId)
	if err := row.Err(); err != nil {
		return err
	}
	if err := row.Scan(&userTableId); err != nil {
		return err
	}

	statement := `
UPDATE games SET winner_id = $1 WHERE chat_id = $2 and date = $3;
`
	_, err := db.Exec(statement, userTableId, chatId, dateFormated)

	return err
}

func AddLox(db *sql.DB, userId int64, chatId int64) error {
	year, month, day := time.Now().Date()
	monthInt := int(month)
	dateFormated := fmt.Sprintf("%d-%02d-%02d", year, monthInt, day)

	var userTableId int64
	row := db.QueryRow(`SELECT id FROM users WHERE user_id = $1;`, userId)
	if err := row.Err(); err != nil {
		return err
	}
	if err := row.Scan(&userTableId); err != nil {
		return err
	}

	statement := `
UPDATE games SET lox_id = $1 WHERE chat_id = $2 and date = $3;
`
	_, err := db.Exec(statement, userTableId, chatId, dateFormated)

	return err
}

func GetWinners(db *sql.DB, chatId int64) ([]GotLucky, error) {
	statement := `
SELECT users.user_id, COUNT(users.user_id) as got_lucky FROM games INNER JOIN users ON games.winner_id = users.id WHERE games.chat_id = $1 GROUP BY games.winner_id ORDER BY got_lucky;
`
	rows, err := db.Query(statement, chatId)
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var winners []GotLucky
	for rows.Next() {
		var user GotLucky
		err = rows.Scan(&user.UserId, &user.Lucky)
		if err != nil {
			return nil, err
		}
		winners = append(winners, user)
	}

	return winners, err
}

func GetLoxes(db *sql.DB, chatId int64) ([]GotLucky, error) {
	statement := `
SELECT users.user_id, COUNT(users.user_id) as got_unlucky FROM games INNER JOIN users ON games.lox_id = users.id WHERE games.chat_id = $1 GROUP BY games.lox_id ORDER BY got_unlucky;
`
	rows, err := db.Query(statement, chatId)
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var loxes []GotLucky
	for rows.Next() {
		var user GotLucky
		err = rows.Scan(&user.UserId, &user.Lucky)
		if err != nil {
			return nil, err
		}
		loxes = append(loxes, user)
	}

	return loxes, err
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
	defer func(rows *sql.Rows) {
		_ = rows.Close()
	}(rows)

	var playerIds []int64
	for rows.Next() {
		var user int64
		err = rows.Scan(&user)
		if err != nil {
			return nil, err
		}
		playerIds = append(playerIds, user)
	}

	return playerIds, nil
}
