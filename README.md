# Argo

A modular group-admin bot for Telegram, written in Go. It keeps its data in PostgreSQL, speaks every language of
`locales/*.json` (the language is chosen per chat), and every feature lives in a module that can be switched off for
the whole bot or for a single chat.

## Features

| Module | Commands and what it does |
|---|---|
| **bans** | `/ban /dban /sban /tban /unban /kick /punch /kickme /punchme`, Unban button |
| **mutes** | `/mute /smute /tmute /stmute /stempmute /unmute /sunmute`, Unmute button |
| **warns** | `/warn /dwarn /warns /unwarn /rmwarn /resetwarns /warnlimit /setwarnlimit /strongwarn /warnmode`, buttons; warn filters `/addwarn /nowarn /warnlist` |
| **blacklists** | `/blacklists /addblacklist /unblacklist /blacklistmode /setblacklistmode /blocklistdelete /unblacklistall`; per-trigger actions, wildcards, `exact:` `prefix:` `sticker:` `stickerpack:`, also checks edited messages |
| **joinblacklists** | `/joinblacklists /blacklist{name,username,bio,channel} /unblacklist{...}`, checked when someone joins |
| **antiflood** | `/flood /setflood /setfloodtimer /setfloodmode /clearflood /antichannel`; message-count and timed limits, albums |
| **antiraid** | `/antiraid /raidlimit /raidtime /raidmode` |
| **locks** | `/lock /unlock /locks /locktypes /lockwarns /lockmode /allowlist /rmallowlist /rmallowlistall`; 41 kinds of locks, bots lock |
| **captcha** | `/captcha /captchamode /captcharules /captchamutetime /captchakick /captchakicktime /setcaptchatext /resetcaptchatext`; button, math, text and picture tests, join requests |
| **greetings** | `/welcome /goodbye /setwelcome /setgoodbye /resetwelcome /resetgoodbye /welcomemute /cleanwelcome /cleanservice`; greetings for the bot's staff |
| **nightmode** | `/nightmode /nighthours /nighttz`; zones such as `Europe/Warsaw` or `UTC+2` |
| **approves** | `/approve /unapprove /approval /approved /disapproveall` |
| **rules** | `/setrules /resetrules /rules [here\|noformat]` |
| **notes** | `/notes /get /save /clear /clearall`, `#name`, media |
| **filters** | `/filters /filter /stop /stopall`; words, "phrases", `prefix:`, `exact:`, groups, formatted replies with media |
| **afk** | `/afk`, `brb`, notices on mentions and replies |
| **reporting** | `/report`, `@admin`, `/reports [on\|off]` |
| **admin** | `/link /gtitle /adminlist /cleanbluetext /pin /unpin /unpinall /staffhelp /adminerror /anonadmin` |
| **promote** | `/promote /fullPromote /demote /title /tag` |
| **purges** | `/del /purge /spurge` |
| **disabling** | `/disable /enable /restrictmodule /unrestrictmodule /disablemodule /enablemodule /disabled` |
| **connection** | `/connect /disconnect /connection`: manage a group from a private chat |
| **gbans** | `/gban /dgban /ungban /gstat /gbans /enforcegban /gimport /gexport`; approval requests for Support staff |
| **federations** | `/newfed /renamefed /delfed /fedtransfer /fedpromote /feddemote /feddemoteme /fednotif /fedreason /subfed /unsubfed /setfedlog /unsetfedlog /fedexport /fedimport /fban /dfban /unfban /clearfban /myfeds /fedinfo /fedadmins /fedsubs /chatfed /joinfed /leavefed /quietfed /fedstat` |
| **logchannel** | `/setlog /unsetlog /logchannel`: a copy of a chat's moderation actions |
| **language** | `/setlang` |
| **backups** | `/export /import /exportitems` (chat owner only) |
| **zombies** | `/zombies`: remove deleted accounts; with `API_ID` and `API_HASH` the whole member list is read over MTProto |
| **stickers** | `/getsticker`, `/kang [emoji]` |
| **fun** | `/pat /slap /hug /roast /decide /flip /roll /rate /ud` |
| **misc** | `/start`, `/help` with categories and pages, `/id`, `/info`, `/tr /dict /time /weather /cash` |
| **root** | `/ping /modules /module /cleanchats /stats /status /leave /speedtest`, staff roles (`/adddev /rmdev /addsudo /rmsudo /addsupport /rmsupport /staff`), `/whitelist /unwhitelist` |

Also: a phone book of the users the bot has seen, chat clean-up (`CHAT_CLEANUP_HOURS`), start and stop notices, a
scheduled log report sent as a `.txt` file to the developer log (`LOG_REPORT_HOURS`), error reports with details in a
file, and a graceful stop.

## Notes

* **MTProto** (optional) is a client logged in with the bot token (gotd/td). It is used only to list the members of a
  chat for `/zombies`. The session is kept in `data/argo-mtproto.json` (a volume in `docker-compose.yml`). A channel
  member list is read with the "recent" filter in pages of 200, so a chat above Telegram's limit for one filter may be
  listed only in part. An offline start fails cleanly after 60 s and `/zombies` falls back to the users the bot has seen.
* `/kang` and `/getsticker` read still pictures with Go's own decoders (PNG, JPEG, WebP) and scale them with
  Catmull-Rom. Animated and video stickers are passed on untouched.
* The log report names every line `bot` or `usage` (commands).
* `/gban` and `/ungban` (staff only) answer in English; `/gstat` and `/gbans` use the chat language. Most federation
  commands are translated, but the federation event notices ("New FedBan", the log lines) are English.
* The join blacklists take triggers as plain words with the wildcards of the blacklist; `exact:` and `prefix:` are not
  parsed there.
* The picture captcha (`/captchamode text2`) is drawn with a small built-in dot-matrix font, so it always works.
* `/antiraid off` ends a running raid before it switches the antiraid off.
* `/status` shows the versions of Go and of the bot library.
* `/info` compares the flags shown in the Status and their order by the translated labels, so it works in every language.
* Telegram and network errors are only logged; every other error goes to the developer log as a file with details.

## Layout

```
cmd/argo/            main: config, database, bot, polling, shutdown
internal/config/     environment settings
internal/db/         pool and schema
internal/i18n/       languages (the JSON files in locales/)
internal/text/       HTML escaping and links
internal/core/       router, middlewares, roles, users, chats, targets, moderation, chat log, flood, locks, nightmode, antiraid, captcha
internal/markup/     formatting of saved messages
internal/blacklist/  trigger matching (hand-written, because Go's regexp has no look-around)
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

The text of the help pages lives in `internal/modules/helpdata.go`, plain Go data you edit by hand.

## Build and run

Needs Go 1.25 or newer.

```sh
go mod download
go build -o argo ./cmd/argo
./argo                   # reads .env
```

Docker: `docker compose up -d --build`. Never run two copies with the same token: both would poll Telegram and fight
over the updates.

`.gitignore` keeps `.env`, `data/`, `vendor/` and build output out of git. `.github/workflows/ci.yml` runs gofmt, vet
and the tests (with a throw-away PostgreSQL) on every push.

## Environment variables

Read from the environment or from a `.env` file (the environment wins).

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
| `SUPPORT_CHAT_USERNAME` | no | `@ArgoRobotSupport` | Appeal chat shown to banned people and at the bottom of `/help` (may be empty) |
| `GITHUB_URL` | no | the project's GitHub | Link shown by `/start` (http or https) |
| `API_ID`, `API_HASH` | no | | From my.telegram.org; with both set `/zombies` lists whole chats through MTProto |
| `LOCALES_DIR` | no | `locales` | Folder with the language files (`/app/locales` in the image) |
| `TELEGRAM_API_URL` | no | api.telegram.org | A local Bot API server (and the tests) |
| `TEST_DATABASE_URL` | tests | local test database | **Wiped** by `go test`, never the real one |

## Database

On start the program runs `CREATE TABLE IF NOT EXISTS` and `ADD COLUMN IF NOT EXISTS` for its 39 tables, so a new
database is set up and an existing one is read as it is; nothing is dropped or rewritten. Take a backup first
(`pg_dump`), and try a new version on a copy (`createdb -T argo argo_try`) before pointing the bot at the real database.

## Tests

```sh
TEST_DATABASE_URL=postgres://user@host/dbname go test -race ./...
```

**The test database is wiped** (`DROP SCHEMA public CASCADE`), use an empty one. Without a database the tests that
need one are skipped. They run the real handlers against a fake Telegram server, so no token is needed:

* `internal/i18n`: every tag of every language is rendered and compared with a golden file
* `internal/markup`, `internal/blacklist`: golden files with the expected output of the saved-message engine and of the trigger matching
* `internal/modules`: scenarios of every command (texts, Telegram calls, database, chat log, buttons)
* `cmd/argo`: starts the program, sends it a command through `getUpdates`, stops it with SIGTERM

The golden files (`testdata/golden.json`) are ordinary test data. If you change those parts on purpose, update them by
hand or regenerate them from the Go output.

## Not yet checked on live Telegram

The tests use a fake server that follows the Bot API as documented, so these are the things to watch on a real run:

* `editMessageText` without buttons (the library sends an empty keyboard to remove them)
* looking up an unknown `@username` (`getChat` with `chat_id=@name`)
* the premium "write as a channel" cases and anonymous admins
* `promoteChatMember` for `/demote`: the library sends `can_restrict_members=false` explicitly, the other rights are left out
* `chat_member` and `chat_join_request` updates (the bot must be an admin); the 3 s wait for a service message that decides whether a `chat_member` update greets
* `setChatPermissions` with `use_independent_chat_permissions` (nightmode) and `restrictChatMember` for the captcha mute
* sending the captcha picture as a multipart upload
* `unpinAllChatMessages`, `deleteMessages` in batches of 100 and `createChatInviteLink` against real chats
* file download for `/gimport` and `/fedimport`, document upload for `/gexport` and `/fedexport`, and `my_chat_member` updates
* `getChat` returning `bio` and `personal_chat` for the join blacklists
* the MTProto connection used by `/zombies`
* the lookup services (translate.googleapis.com, dictionaryapi.dev, open-meteo.com, open.er-api.com)
