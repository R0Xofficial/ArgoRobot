# Argo (Go port)

A port of the Python Argo to Go, kept on its own branch. It uses the **same PostgreSQL schema** and the **same
`locales/*.json` files** as the Python version, so both can run on the same database (never at the same time with
the same token).

## What is ported

| Part | State |
|---|---|
| Configuration (`.env` / environment, same names) | done |
| PostgreSQL (pgx), schema of the ported tables, same as Python | done |
| Languages: all of `locales/*.json`, per-chat language, plurals, fallback to English | done, checked against Python on 6940 texts |
| Roles (Owner from config, staff from the `roles` table) | done (batch 2: `/adddev /rmdev /addsudo /rmsudo /addsupport /rmsupport /staff`) |
| Phone book of users, chats, who-was-where, chat removal and migration to a supergroup | done |
| Target resolution (reply, text mention, @username, id, channels) | done |
| Moderation core: who may act, immunity, refusals, `/adminerror`, `/staffhelp`, `/anonadmin`, chat log, staff note | done |
| **bans**: `/ban /dban /sban /tban /unban /kick /punch /kickme /punchme`, Unban button | done |
| **warns**: `/warn /dwarn /warns /unwarn /rmwarn /resetwarns /warnlimit /setwarnlimit /strongwarn /warnmode`, buttons | done |
| **language**: `/setlang`, buttons | done |
| **mutes**: `/mute /smute /tmute /stmute /stempmute /unmute /sunmute`, Unmute button | done (batch 1) |
| **blacklists**: `/blacklists /addblacklist /unblacklist /blacklistmode /setblacklistmode /blocklistdelete /unblacklistall`, enforcement (also on edited messages), per-trigger actions, wildcards, `exact:` `prefix:` `sticker:` `stickerpack:` | done (batch 1) |
| **warn filters**: `/addwarn /nowarn /warnlist` | done (batch 1) |
| **approves**: `/approve /unapprove /approval /approved /disapproveall` | done (batch 1); lifting a global/federation ban on approval is hooked up with those modules |
| **rules**: `/setrules /resetrules /rules [here|noformat]`, `/start rules_<chat>` link, media | done (batch 1) |
| **notes**: `/notes /get /save /clear /clearall`, `#name`, media | done (batch 1) |
| Saved-message engine: HTML formatting, fillings, options, buttons, random variants, media (`internal/markup`, `core/sender.go`) | done, checked against Python on 6800 cases |
| **root**: `/ping /modules /module /cleanchats /stats /status /leave /speedtest`, staff roles, `/staff` with buttons, `/whitelist /unwhitelist` (always English, Developer/Sudo/Support only, as in Python) | done (batch 2) |
| **disabling**: `/disable /enable /restrictmodule /unrestrictmodule /disablemodule /enablemodule /disabled` | done (batch 2) |
| **connection**: `/connect /disconnect /connection`, `/start connect_<chat>`, group commands from a private chat, connections end when the module is turned off | done (batch 2) |
| **admin**: `/link /gtitle /adminlist /cleanbluetext /pin /unpin /unpinall /staffhelp /adminerror /anonadmin` | done (batch 2) |
| **promote**: `/promote /fullPromote /demote /title /tag` | done (batch 2) |
| **purges**: `/del /purge /spurge` | done (batch 2) |
| Module registry: modules can be turned off for the whole bot (`/module`, kept in the `modules` table) or for one chat, commands can be switched off for members, "protected" modules, command-clean-up (`/cleanbluetext`) | done (batch 2) |
| Chat cleanup (`/cleanchats` and the schedule set by `CHAT_CLEANUP_HOURS`, default 24, 0 = off) | done (batch 2) |
| **antiflood**: `/flood /setflood /setfloodtimer /setfloodmode /clearflood /antichannel`, message-count and timed flood limits, albums, "delete all flood messages" | done (batch 3) |
| **locks**: `/lock /unlock /locks /locktypes /lockwarns /lockmode /allowlist /rmallowlist /rmallowlistall`, 41 kinds of locks, allowlist, bots lock, checks edited messages too | done (batch 3) |
| **antiraid**: `/antiraid /raidlimit /raidtime /raidmode`, counting joins from service messages and `chat_member` updates | done (batch 3) |
| **nightmode**: `/nightmode /nighthours /nighttz`, zones (`Europe/Warsaw`, `UTC+2`), the saved permissions survive a restart, sweep every 30 s | done (batch 3) |
| **greetings**: `/welcome /goodbye /setwelcome /setgoodbye /resetwelcome /resetgoodbye /welcomemute /cleanwelcome /cleanservice`, staff greetings, `#JOINED`/`#LEFT` in the chat log, joins/leaves seen only through `chat_member` updates | done (batch 3) |
| **captcha**: `/captcha /captchamode /captcharules /captchamutetime /captchakick /captchakicktime /setcaptchatext /resetcaptchatext`, button / math / text / picture tests, rules step, join requests, kick and unmute sweep (every 20 s) | done (batch 3) |
| **gbans**: `/gban /dgban /ungban /gstat /gbans /enforcegban /gimport /gexport`, approval requests for Support staff (`gban:` buttons, sudo only), enforcement on messages, joins and `chat_member` updates, background lifting of bans, lifting on approval | done (batch 4a) |
| **federations**: `/newfed /renamefed /delfed /fedtransfer /fedpromote /feddemote /feddemoteme /fednotif /fedreason /subfed /unsubfed /setfedlog /unsetfedlog /fedexport /fedimport /fban /dfban /unfban /clearfban /myfeds /fedinfo /fedadmins /fedsubs /chatfed /joinfed /leavefed /quietfed /fedstat`, subscriptions, enforcement, a chat leaves its federation when the bot loses ban rights | done (batch 4a) |
| **logchannel**: `/setlog /unsetlog /logchannel` | done (batch 4a) |
| **joinblacklists**: `/joinblacklists /blacklist{name,username,bio,channel} /unblacklist{...}`, checks on joins | done (batch 4a) |
| **filters**: `/filters /filter /stop /stopall`, triggers as words, "phrases", `prefix:`, `exact:` and `(a, b, "c d")` groups, formatted replies with media, cooldown 5 s | done (batch 4b) |
| **afk**: `/afk`, `brb`, notice on mentions and replies, "back" notice, photo/GIF/video with the status | done (batch 4b) |
| **reporting**: `/report`, `@admin`, `/reports [on\|off]` (in a group and in private for admin alerts) | done (batch 4b) |
| **misc**: `/start` (incl. `help`, `help_<module>`, `connect_<chat>`, `cap_<id>` deep links), `/help` with categories, sections, extra pages and notes (the help data lives in `internal/modules/helpdata.go`, plain Go data you edit by hand), `/id`, `/info` (flags and notes from the modules), `/tr /dict /time /weather /cash` | done (batch 4b) |
| **zombies**: `/zombies`, with MTProto (`API_ID` + `API_HASH`) the whole member list is read, otherwise the users the bot has seen are checked one by one with `getChatMember`; in the background | done (batch 4b) |
| **MTProto** (optional): a client logged in with the bot token (gotd/td), only to list the members of a chat; no updates; the session is kept in `data/argo-mtproto.json` (a volume in `docker-compose.yml`) | done |
| **backups**: `/export`, `/import`, `/exportitems` (chat owner only; the export goes to the owner's private chat, JSON with a version, per-table validation on import) | done (batch 4b) |
| **fun**: `/pat /slap /hug /roast /decide /flip /roll /rate /ud` | done (batch 4b) |
| **stickers**: `/getsticker` (a still sticker comes back as PNG), `/kang [emoji]` (stickers and pictures, packs `s|a|v<user>_<n>_by_<bot>`) | done (batch 4b) |
| Administrators of a group are recorded in the background (at most every 6 h per group, at once when the bot is added) | done (batch 4b) |
| Log report: the INFO+ log is sent to the developer log as a `.txt` file every `LOG_REPORT_HOURS` hours (default 6, 0 = off) and when the bot stops; a report that failed is kept for the next one | done (batch 4b) |
| Start/stop notices, start-up retry for network timeouts, graceful stop | done |

Everything of the Python bot is ported, except what is listed under the differences below.

Differences from the Python version that come from what is not ported yet:

* MTProto: the session file is a new one (gotd's JSON, not Telethon's `.session`), so the first start logs in again.
  A channel member list is read with the "recent" filter in pages of 200 (Telethon also splits very large chats into
  several queries to get past Telegram's limit of what one filter returns; here a chat above that limit may be listed
  only in part, and then /zombies finds only what was listed). **The connection was never tried against Telegram**: only
  the code that reads the pages is tested, with a fake; an offline start fails cleanly after 60 s and /zombies falls back
  to the users the bot has seen.
* A backup import accepts NULL for the range columns (the Python version does the same); the values are checked per table.
* `/kang` and `/getsticker` read still pictures with Go's own decoders (PNG, JPEG, WebP through `golang.org/x/image`) and
  scale with Catmull-Rom instead of Pillow's Lanczos, so the pixels differ slightly. Animated and video stickers are
  passed on untouched, as in Python.
* The log report names every line `bot` or `usage` (commands) instead of the name of the Python logger.

* `/gban` and `/ungban` (staff only) answer in English, as in Python; `/gstat` and `/gbans` use the chat language. Most federation
  commands (`/chatfed`, `/fedinfo`, ...) are translated, but the federation event notices ("New FedBan", the log lines) are
  English, again as in Python.
* The join blacklists take triggers as plain words with the wildcards of the blacklist; `exact:`/`prefix:` are not parsed there.
* The picture captcha (`/captchamode text2`) is drawn with a small built-in dot-matrix font (the Python version uses
  Pillow and the system fonts), so it always works but looks different.
* `/antiraid off` ends a running raid before it switches the antiraid off (in Python the order is the other way round and
  the raid is left waiting until the antiraid is turned on again).
* `/status` shows the Go version and the library instead of the Python and aiogram versions.
* `/info` compares the flags that are shown in the Status ("Approved", "Captcha Pending") and its order by the translated labels, so it also
  works in the other languages (the Python version compares English words).
* The lookups (`/tr /dict /time /weather /cash`) need network access to free public services and were only run against stubs. `/time` knows the
  same zone names as Python (`internal/core/tzlist.go`, generated).

## Layout

```
cmd/argo/            main: config, database, bot, polling, shutdown
internal/config/     environment settings
internal/db/         pool and schema
internal/i18n/       languages (same JSON files as Python)
internal/text/       HTML escaping and links
internal/core/       router, middlewares, roles, users, chats, targets, moderation, chat log, flood, locks, nightmode, antiraid, captcha
internal/markup/     formatting of saved messages (port of core/markup.py)
internal/blacklist/  trigger matching (port of core/blacklist.py, hand-written because Go's regexp has no look-around)
internal/modules/    bans.go, mutes.go, root.go, admin.go, promote.go, ...   <- a module is one file with a register function
internal/testutil/   fake Telegram Bot API + test environment
```

A module registers its commands and buttons in `modules.Register`:

```go
r.Module("bans", "Ban commands for chat admins")   // what is registered next belongs to this module
r.Command(cmdBan, "ban", "dban", "sban")
r.CommandRole(core.Developer, cmdStats, "stats")  // only for people holding at least the role
r.Button("undo:ban:", cbUnban)
```

## Build and run

Needs Go 1.24 or newer. The zip contains a `vendor/` directory with every dependency (including `golang.org/x/image` and
`github.com/gotd/td`, copied by hand because the Go proxy was not reachable where this was written), so `go build` works
offline. `go mod tidy` (with network) rewrites `go.sum` and may raise the `go` line of `go.mod` if a dependency asks for it.

```sh
go mod tidy              # once, with network: writes go.sum (commit both files)
go build -o argo ./cmd/argo
./argo                   # reads .env, like the Python version
```

Docker: `docker compose up -d --build`. The Go version lives on its own branch, so `Dockerfile` and `docker-compose.yml`
are the ones of this branch. The variables are the same as in the Python version.

`.gitignore` keeps `.env`, `data/` and build output out of git; `vendor/` is meant to be committed (or delete it and use `go mod download`).
`.github/workflows/ci.yml` runs gofmt, vet and the tests (with a throw-away PostgreSQL) on every push.

## Environment variables

Read from the environment or from a `.env` file (the environment wins). The names are those of the Python version.

| Variable | Needed | Default | Meaning |
|---|---|---|---|
| `BOT_TOKEN` | yes | | Token from BotFather |
| `OWNER_ID` | yes | | Telegram id of the owner (root of the bot) |
| `DATABASE_URL` | yes | | PostgreSQL, e.g. `postgresql://user:pass@host:5432/argo` |
| `DEV_LOG_CHAT` | no | owner's private chat | Chat for the developer log (start/stop, errors, log reports) |
| `DEV_LOG_THREAD_ID` | no | | Topic of `DEV_LOG_CHAT`, if it is a forum |
| `LOG_CHAT_ID` | no | developer log | Chat for the user log (staff changes, global bans) |
| `LOG_THREAD_ID` | no | | Topic of `LOG_CHAT_ID` |
| `LOG_LEVEL` | no | `INFO` | `DEBUG`, `INFO`, `WARNING`, `ERROR` |
| `LOG_REPORT_HOURS` | no | `6` | How often the log goes to the developer log as a `.txt` file; `0` = off |
| `CHAT_CLEANUP_HOURS` | no | `24` | How often the stored chats are checked against Telegram; `0` = off |
| `COMMAND_PREFIXES` | no | `/!` | Characters that start a command |
| `SUPPORT_CHAT_USERNAME` | no | `@ArgoRobotSupport` | Appeal chat shown to banned people and in `/start` (may be empty) |
| `GITHUB_URL` | no | the project's GitHub | Link shown by `/start` (http or https) |
| `API_ID`, `API_HASH` | no | | From my.telegram.org; with both set `/zombies` lists whole chats through MTProto |
| `LOCALES_DIR` | no | `locales` | Folder with the language files (`/app/locales` in the image) |
| `TELEGRAM_API_URL` | no | api.telegram.org | A local Bot API server (and the tests) |
| `TEST_DATABASE_URL` | tests | local test database | **Wiped** by `go test`, never the real one |

## Existing database

The tables, columns, types, defaults, indexes and constraints are exactly those of the Python version (39 tables, compared
with `information_schema`, `pg_indexes` and `pg_constraint`), and on start the program only runs `CREATE TABLE IF NOT EXISTS`
and `ADD COLUMN IF NOT EXISTS`, so an old database is read as it is and nothing is dropped or rewritten. Take a backup
first (`pg_dump`), and try it on a copy (`createdb -T argo argo_try`) before pointing the bot at the real one.

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
* `promoteChatMember` for `/demote`: the library can only send `can_restrict_members=false` explicitly, the other rights
  are left out (Telegram reads a missing right as false, but this was not seen on the real API)
* `chat_member` and `chat_join_request` updates (the bot must be an admin, and the update types are requested in `getUpdates`);
  the 3 s wait for a service message that decides whether a `chat_member` update greets
* `setChatPermissions` with `use_independent_chat_permissions` (nightmode) and `restrictChatMember` for the captcha mute
* sending the captcha picture as a multipart upload
* `unpinAllChatMessages`, `deleteMessages` in batches of 100 and `createChatInviteLink` against real chats
* `getFile`/file download for `/gimport` and `/fedimport`, document upload for `/gexport` and `/fedexport`, and `my_chat_member` updates (federation rights check)
* `getChat` returning `bio` and `personal_chat` for the join blacklists
* the lookup services (translate.googleapis.com, dictionaryapi.dev, open-meteo.com, open.er-api.com): their answers were not seen by the Go code
* behaviour under load (every update runs in its own goroutine, caches are mutex-protected, `-race` is clean)

## Tests

`go test -race ./...` needs a PostgreSQL it may wipe (`TEST_DATABASE_URL`, default
`postgres://postgres@/argotest?host=/tmp&port=54329`). Besides scenario tests against a fake Telegram server, three
golden files (`internal/i18n/testdata/golden.json`, `internal/markup/testdata/golden.json`,
`internal/blacklist/testdata/golden.json`) hold the expected output of the texts, the saved-message engine and the
blacklist. They were made once from the Python version and are now ordinary test data, so nothing here needs Python.
If you change those parts on purpose, update the golden files by hand or regenerate them from the Go output.
