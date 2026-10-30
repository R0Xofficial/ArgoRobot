# Argo (@ArgoRobot)

**A modular Telegram group-administration bot.**

Argo keeps groups tidy and safe: bans, mutes and warnings, spam filters, welcome messages, notes, locks, federations that share one ban list across many chats, and global bans for the worst offenders. It is written in Python 3 on [aiogram](https://github.com/aiogram/aiogram) 3 and stores everything in PostgreSQL.

Repository: <https://github.com/R0Xofficial/ArgoRobot>

## Contents

1. [What it does](#1-what-it-does)
2. [Requirements](#2-requirements)
3. [Installation](#3-installation)
4. [Configuration](#4-configuration)
5. [Roles](#5-roles)
6. [How bans reach people](#6-how-bans-reach-people)
7. [Federations](#7-federations)
8. [Managing a group from a private chat](#8-managing-a-group-from-a-private-chat)
9. [Switching commands off](#9-switching-commands-off)
10. [Logs](#10-logs)
11. [Project layout and adding modules](#11-project-layout-and-adding-modules)
12. [Good to know](#12-good-to-know)

## 1. What it does

| Area | Features |
| --- | --- |
| **Moderation** | Bans, temporary bans, kicks, mutes, temporary mutes, warnings with a limit and an action, silent and "delete the message too" variants (`/sban`, `/smute`, `/dban`, `/dwarn` ...), `/kickme`, removing zombies (deleted accounts) with `/zombies`, bulk deleting messages (`/purge`). |
| **Automatic protection** | Antiflood, locks (urls, media, forwards ...), blacklisted words and phrases, warn filters, join blacklists (names, usernames, bios, channels), approved users that automatic measures skip, user reports to the admins. |
| **Chat tools** | Welcome and goodbye messages (with media and buttons), rules, notes, filters, promote/demote with custom titles, pins, invite links, AFK, backups of a chat's data, sticker tools, light commands for fun. |
| **Bans across chats** | Federations (one ban list for many chats, with subscriptions) and global bans (`/gban`) decided by the bot's staff. |
| **Housekeeping** | A log chat for moderation actions, per-chat switches, switching single commands or whole modules off, managing a group from a private chat (`/connect`). |

Everything is described by the bot itself: send `/help` (or `/help <topic>`, for example `/help notes`) and it opens the right page.

## 2. Requirements

- Python 3.12 or newer (the Docker image uses 3.12)
- PostgreSQL
- A bot token from [@BotFather](https://t.me/BotFather)
- Your Telegram user id (the bot's owner)

Python packages (`requirements.txt`): aiogram, pydantic-settings, asyncpg, tzdata, Pillow.

## 3. Installation

### Option A: Docker Compose (recommended)

1. Copy `.env.example` to `.env` and fill in `BOT_TOKEN`, `OWNER_ID` and `DATABASE_URL` (see [Configuration](#4-configuration)).
2. The compose file expects an external Docker network called `postgres-internal` where your PostgreSQL container lives. Adjust `docker-compose.yml` if yours is named differently.
3. Start it:

   ```bash
   docker compose up -d --build
   ```

In Portainer, paste the same names and values under the stack's *Environment variables* instead of using a `.env` file. A `$` inside a value must be written as `$$`.

### Option B: directly on a server

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
cp .env.example .env        # then edit it
python main.py
```

Use `localhost` as the database host in `DATABASE_URL`.

The first start creates every table it needs. Later versions add new columns and tables by themselves, so updating means: replace the files and restart.

Before the bot can do its work in a group, add it and make it an admin. Which rights it needs depends on what you want: ban users, delete messages, pin messages, invite users, change chat info.

## 4. Configuration

Settings come from environment variables or from a `.env` file next to `config.py`. Never commit `.env`.

**Required**

| Variable | Meaning |
| --- | --- |
| `BOT_TOKEN` | Token from @BotFather. |
| `OWNER_ID` | Your Telegram user id. The owner cannot be demoted by any command. |
| `DATABASE_URL` | `postgresql://user:password@host:5432/dbname` (URL-encode special characters in the password). |

**Optional**

| Variable | Meaning |
| --- | --- |
| `LOG_LEVEL` | `DEBUG`, `INFO`, `WARNING` or `ERROR`. Default `INFO`. |
| `DEV_LOG_CHAT` | Chat for start/stop messages and error reports. Empty: sent to the owner privately. |
| `DEV_LOG_THREAD_ID` | Topic id, if that chat has topics. |
| `LOG_CHAT_ID` | Public log: promotions, demotions and other information everyone may read. Empty: falls back to the developer log. |
| `LOG_THREAD_ID` | Topic id, if that chat has topics. |
| `CHAT_CLEANUP_HOURS` | How often the bot forgets chats it was removed from. Default `24`, `0` turns it off (`/cleanchats` still works). |
| `APPEAL_CHAT_USERNAME` | Where people can appeal a global ban, for example `@MyAppealChat`. Shown when a globally banned user is removed. |
| `COMMAND_PREFIXES` | Characters that start a command. Default `/!`. In groups `!` commands only reach the bot if it is an admin or privacy mode is off (BotFather: `/setprivacy`). |
| `GITHUB_URL` | Repository button in `/start`. |

## 5. Roles

The bot has its own staff, separate from chat admins:

```
Support  <  Sudo  <  Developer  <  Owner
```

| Role | What it can do |
| --- | --- |
| **Owner** | Set only by `OWNER_ID`, never stored, cannot be removed. |
| **Developer** | Same powers as the owner, but can be demoted. |
| **Sudo** | Approves global bans, manages the whitelist, and may help moderate chats (a chat's owner can turn that off with `/staffhelp`). |
| **Support** | Asks for global bans. A request is approved or declined by Sudo. |

Roles are managed with `/addsudo`, `/rmsudo`, `/addsupport`, `/rmsupport`, `/adddev` and `/rmdev`. `/staff` shows who holds what. Staff can never change a chat's settings: those belong to the chat's own admins and owner.

Chat admins use the moderation commands of their chat. Each command checks the exact admin right it needs.

## 6. How bans reach people

Global bans and federation bans are enforced **when the banned person shows up**, not by sweeping every chat at the moment of the ban. A banned user is removed in a chat where the bot is an admin as soon as they

- send a message or edit one,
- join or leave the chat,
- react to a message.

The bot says why ("This user is globally banned ...", with the reason) so chat admins are never left wondering. Chat admins, approved users and the bot's staff are left alone.

**Global bans (`/gban`).** Support asks, Sudo approves with a button (or declines). A request that nobody answered stops blocking new ones after 7 days. `/ungban` lifts the ban and unbans the person where the bot had banned them. A chat's owner can switch enforcement off for that chat with `/gbans off`. `/gstat` shows a user's status.

**Why it works this way.** It needs far fewer requests to Telegram, covers chats the person joins later, and every removal comes with a visible reason.

## 7. Federations

A federation is a ban list shared by many chats.

| Who | Commands |
| --- | --- |
| Owner | `/newfed`, `/renamefed`, `/delfed`, `/fedtransfer`, `/fedpromote` (alias `/fpromote`), `/feddemote` (alias `/fdemote`), `/setfedlog` |
| Admins | `/fban`, `/unfban`, `/fedreason`, `/fedexport`, `/fedimport` |
| Chats | `/joinfed`, `/leavefed`, `/quietfed` (chat owner) |
| Anyone | `/fedinfo`, `/fedadmins`, `/fedstat` (alias `/fbanstat`) |

**Subscriptions.** `/subfed` makes your federation inherit the bans of another one, `/unsubfed` undoes it. `/fedsubs` shows both directions.

**Log events.** A chat joined or left, an admin or owner changed, someone subscribed, a new ban: each goes to the federation's log chat with its own title: New FedChat, Un-FedChat, FedSubscribed, New FedSubscriber, New Fed Promotion, New Fed Demotion, New FedOwner, New FedBans and so on.

**Lost rights.** When the bot loses the right to ban users in a chat, that chat leaves its federation by itself and says so. Give the right back and use `/joinfed` to join again.

## 8. Managing a group from a private chat

1. In the group, send `/connect`. The bot answers with a button. (Or, in a private chat with the bot: `/connect <chat id or @username>`.)
2. From then on, the group's admin commands you send to the bot privately (`/ban @someone`, `/lock url`, `/filters` ...) act on that group, and the bot answers privately.
3. `/connection` shows where you are connected, `/disconnect` ends it.

Your rights in the group are checked on every command, exactly as if you had sent it there. Commands that need a reply to a message, delete your command, or show a saved message in the chat must still be sent in the group.

## 9. Switching commands and modules off

**For members only.** Chat admins can switch off commands they do not want members to use:

```
/disable slap            /enable slap
/restrictmodule fun       /unrestrictmodule fun
/disabled                (shows what is off)
```

Members are ignored; admins can still use the command. Aliases are switched off together with the command.

**Completely.** The chat's owner can turn a module off in the chat for everybody, admins included. Its commands stop and so does everything it does by itself (filters, locks, flood control, welcome messages ...):

```
/disablemodule fun           /enablemodule fun
/disablemodule all           /enablemodule all
```

`/disablemodule all` turns off every module except `gbans`, for a chat that only wants global bans. `/disabled` shows all three kinds of switches.

`/help`, `/start`, `/connect` and the commands of this page can never be switched off.

Bot-wide, the owner and developers can turn whole modules on and off for every chat with `/module` and list them with `/modules`.

## 10. Logs

- **Console / container log:** one line per command or button press (never message text), plus warnings and errors.
- **Developer log** (`DEV_LOG_CHAT`): start and stop messages and error reports with the traceback as a file.
- **Public log** (`LOG_CHAT_ID`): staff changes and other public notices.
- **Chat log:** `/setlog <chat id | @username | reply to a forwarded channel post>` makes the bot copy that chat's moderation actions (bans, mutes, warns, locks, promotions ...) to a chat of the admins' choice. `/unsetlog` and `/logchannel` manage it.

## 11. Project layout and adding modules

```
main.py                 Entry point: builds the bot, loads modules, polls.
config.py               Settings read from the environment.
core/                   Shared machinery: database, roles, moderation checks,
                        federations and global bans, help index, middlewares.
modules/                One file per feature. Everything in this folder is
                        found and loaded automatically.
Dockerfile, docker-compose.yml, .env.example
```

A module is a file in `modules/` with a `router` (an aiogram `Router`). It may also define:

| Name | Meaning |
| --- | --- |
| `ABOUT` | Text shown on the module's help page. |
| `HELP` | List of `(usage, description[, who can use it])` entries. |
| `PRIORITY` | Lower numbers are checked first (default 0). |
| `PROTECTED` | `True` if it must never be switched off. |

Drop a new file into `modules/` and restart: no other file needs to change. A module that fails to load is logged and skipped; it never stops the bot from starting. Write commands with `core.filters.cmd()`, so that prefixes, case-insensitivity and the "switch off" feature work for them, and answer with `core.helpers.reply()`.

All text the bot writes is in English.

## 12. Good to know

- Telegram does not let bots list a group's members. Features that sweep a chat (`/zombies`, global bans reaching old members) work on people the bot has seen.
- Reactions and member changes reach the bot only in chats where it is an admin.
- Messages saved before the formatting rules changed (notes, filters, rules, welcome) may need to be saved again.
- Back up your PostgreSQL database. `/export` (in a chat) saves that chat's own settings and data to a file and `/import` puts them back.
