# LoxBot

A Telegram bot you add to a group chat to decide who is today's **"Лох дня"** (Lox of the day) —
and, conversely, today's **"Красавчик дня"** (handsome of the day). Each pick is fixed for the
day and per chat, and the bot keeps a running tally.

## Commands

| Command | What it does |
| --- | --- |
| `/start` | Show a short intro. |
| `/register` | Join the game in this chat. |
| `/leave` | Leave the game in this chat. |
| `/run` | Pick today's *Красавчик дня*. Re-running the same day shows the existing pick. |
| `/lox` | Pick today's *Лох дня*. Re-running the same day shows the existing pick. |
| `/stats` | Leaderboard of how often each player was *Красавчик дня*. |
| `/loxstats` | Leaderboard of how often each player was *Лох дня*. |

Commands also work with an `@botname` suffix (e.g. `/run@MyLoxBot`).

## Configuration

The bot is configured through environment variables:

| Variable | Description |
| --- | --- |
| `BOT_KEY` | Telegram Bot API token from [@BotFather](https://t.me/BotFather). |
| `WEBHOOK_URL` | Public HTTPS URL Telegram will POST updates to. |

The bot runs in **webhook mode** and listens on `:1080`, so `WEBHOOK_URL` must be a public
HTTPS endpoint that forwards to that port (e.g. via a reverse proxy).

### About `.env`

The application code does **not** read `.env` itself. The file is only consumed by the
runtime in the two supported ways of starting the bot:

- **Docker** — passed in with `--env-file .env` (or `env_file:` in Compose).
- **IDE (GoLand/IntelliJ)** — loaded by the run configuration's environment settings.

Copy `.env.example` to `.env` and fill in your values:

```sh
cp .env.example .env
```

## Database

State is stored in SQLite. The database lives in a `database/` directory **next to the
compiled binary** — i.e. `<binary-dir>/database/database.sqlite`. Tables are created
automatically on first run.

- In **Docker** this resolves to `/app/database/database.sqlite`, which is mounted as a
  volume (see `VOLUME` in the `Dockerfile`) so data survives container restarts.
- When run from the **IDE**, it sits next to the binary the IDE builds.

## Running with Docker

```sh
docker build -t loxbot .
docker run -d \
  --env-file .env \
  -p 1080:1080 \
  -v loxbot-data:/app/database \
  loxbot
```

The named volume `loxbot-data` keeps the SQLite database across restarts.

## Running from the IDE

1. Copy `.env.example` to `.env` and fill in `BOT_KEY` and `WEBHOOK_URL`.
2. In the run configuration, point the environment settings at the `.env` file
   (e.g. the *EnvFile* plugin in GoLand, or add the variables manually).
3. Run `main.go`.

Note that the database is created next to the **compiled binary**, and IDEs build to their
own locations (e.g. a `.cache` directory), so the SQLite file may not land in the project
directory. Check the run configuration's output path if you need to find it.
