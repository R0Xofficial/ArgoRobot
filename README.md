# Argo in Go (prototype)

A port of Argo to Go, living next to the Python version on its own branch. It is a **prototype**: the core and
three modules, enough to judge the approach. It uses the **same PostgreSQL schema** and the **same
`locales/*.json` files** as the Python version, so both can run on the same database (never at the same time with
the same token).

## What is ported

| Part | State |
|---|---|
| Configuration (`.env` / environment, same names) | done |
| PostgreSQL (pgx), schema of the ported tables, same as Python | done |
| Languages: all of `locales/*.json`, per-chat language, plurals, fallback to English | done, checked against Python on 6940 texts |
| Roles (Owner from config, staff from the `roles` table) | read side done (no `/addsudo` etc. yet) |
| Phone book of users, chats, who-was-where, chat removal and migration to a supergroup | done |
| Target resolution (reply, text mention, @username, id, channels) | done |
| Moderation core: who may act, immunity, refusals, `/adminerror`, `/staffhelp`, `/anonadmin`, chat log, staff note | done |
| **bans**: `/ban /dban /sban /tban /unban /kick /punch /kickme /punchme`, Unban button | done |
| **warns**: `/warn /dwarn /warns /unwarn /rmwarn /resetwarns /warnlimit /setwarnlimit /strongwarn /warnmode`, buttons | done |
| **language**: `/setlang`, buttons | done |
| Start/stop notices, start-up retry for network timeouts, graceful stop | done |

Not ported yet (the rest of the Python bot): warn filters (`/addwarn /nowarn /warnlist`, they need the blacklist),
disabled commands, `/connect`, `/cleanbluetext`, module registry and `/modules`, staff commands (`/addsudo` ...),
gbans, federations, captcha, greetings, locks, flood, notes, filters, reports, the periodic admin refresh and
chat cleanup, MTProto, the log report file. Commands of unported modules are simply not answered.

## Layout

```
cmd/argo/            main: config, database, bot, polling, shutdown
internal/config/     environment settings
internal/db/         pool and schema
internal/i18n/       languages (same JSON files as Python)
internal/text/       HTML escaping and links
internal/core/       router, middlewares, roles, users, chats, targets, moderation, chat log
internal/modules/    bans.go, warns.go, language.go   <- a module is one file with a register function
internal/testutil/   fake Telegram Bot API + test environment
```

A module registers its commands and buttons in `modules.Register`:

```go
r.Command(cmdBan, "ban", "dban", "sban")
r.Button("undo:ban:", cbUnban)
```

## Build and run

Needs Go 1.24 or newer.

```sh
go mod tidy              # once: resolves the versions and writes go.sum (commit both files)
go build -o argo ./cmd/argo
./argo                   # reads .env, like the Python version
```

Docker: `docker compose -f docker-compose.go.yml up -d --build` (use it **instead of** `docker-compose.yml`).
The variables are the same; the ones the prototype does not use are ignored.

On the Go branch add `/vendor/` and `/bin/` to `.gitignore`.

## Tests

```sh
TEST_DATABASE_URL=postgres://user@host/dbname go test -race ./...
```

**The test database is wiped** (`DROP SCHEMA public CASCADE`), use an empty one. Without a database the tests that
need one are skipped. They run the real handlers against a fake Telegram server, so no token is needed:

* `internal/i18n`: every tag of every language is rendered and compared with what the Python version renders
* `internal/modules`: scenarios of every ported command (texts, Telegram calls, database, chat log, buttons)
* `cmd/argo`: starts the program, sends it a command through `getUpdates`, stops it with SIGTERM

## What was NOT checked

Nothing has run against the real Telegram API or a production database. The fake server follows the Bot API as
documented, so these are the things to watch on the first real run:

* `editMessageText` without buttons (the library sends an empty keyboard to remove them)
* looking up an unknown `@username` (`getChat` with `chat_id=@name`)
* the premium "write as a channel" cases and anonymous admins
* behaviour under load (every update runs in its own goroutine, caches are mutex-protected, `-race` is clean)
