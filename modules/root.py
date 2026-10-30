"""Core bot commands"""

import asyncio
import time

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from config import settings
from core import chats, database, gbans, immunity, info_hooks, logs, registry, roles, speedtest, whitelist
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.roles import Role
from core.targets import TargetError, display_name, resolve_target
from core.text import esc, user_link, user_ref

# Protected modules cannot be disabled with /module, so the commands in this
# file (including /module itself) are always available.
PROTECTED = True

# Commands shown in /help (see core/help_index.py).
ABOUT = """
Commands for running the bot itself. Most of them are for my staff, so you only see the ones your role allows.

Staff roles, from lowest to highest: Support, Sudo, Developer, Owner. The Owner grants and removes Developers; Developers grant and remove Sudo and Support users.

The whitelist shields people from global actions: someone on it can't be globally banned, in any chat I'm in. It is different from /approve, which only works in one chat. Sudo and up manage it.
"""

HELP = [
    ("/ping", "Check that I'm alive and how fast I answer."),
    ("/modules", "List every module and whether it is enabled.", Role.DEVELOPER),
    ("/module <enable|disable> <name>", "Turn a module on or off. A disabled module stops answering and disappears from /help. Core modules can't be disabled.", Role.DEVELOPER),
    ("/cleanchats", "Forget the chats I have been removed from and report how many were dropped.", Role.DEVELOPER),
    ("/stats", "Show what I know: users, chats, bans and more.", Role.DEVELOPER),
    ("/leave", "Make me leave the chat where you send it.", Role.DEVELOPER),
    ("/speedtest", "Measure the ping, download and upload speed of my server.", Role.DEVELOPER),
    ("/staff", "Show who holds each staff role.", Role.SUPPORT),
    ("/adddev <user>", "Grant the Developer role.", Role.OWNER),
    ("/rmdev <user>", "Remove the Developer role.", Role.OWNER),
    ("/addsudo <user>", "Grant the Sudo role.", Role.DEVELOPER),
    ("/rmsudo <user>", "Remove the Sudo role.", Role.DEVELOPER),
    ("/addsupport <user>", "Grant the Support role.", Role.DEVELOPER),
    ("/rmsupport <user>", "Remove the Support role.", Role.DEVELOPER),
    ("/whitelist <user>", "Protect a user from global bans.", Role.SUDO),
    ("/unwhitelist <user>", "Remove a user's protection.", Role.SUDO),
]

router = Router(name="root")

CB_STAFF_HOME = "staff:home"
CB_STAFF_ROLE = "staff:role:"

# <user> in the commands below: reply to the person, or give an id or @username.
GRANT_COMMANDS = {"adddev": Role.DEVELOPER, "addsudo": Role.SUDO, "addsupport": Role.SUPPORT}
REVOKE_COMMANDS = {"rmdev": Role.DEVELOPER, "rmsudo": Role.SUDO, "rmsupport": Role.SUPPORT}

# /staff buttons, in display order.
STAFF_BUTTONS = [
    (Role.OWNER, "Owner"),
    (Role.DEVELOPER, "Developers"),
    (Role.SUDO, "Sudos"),
    (Role.SUPPORT, "Supports"),
]
STAFF_TEXT_LIMIT = 3800  # Telegram allows 4096 characters per message


# --- /ping ------------------------------------------------------------------

@router.message(cmd("ping"))
async def cmd_ping(message: Message) -> None:
    # Latency is the round trip of sending a message to Telegram.
    started = time.perf_counter()
    sent = await reply(message, "<b>Pong!</b>")
    latency_ms = (time.perf_counter() - started) * 1000
    await edit(sent, f"<b>Pong!</b>\nLatency: <code>{latency_ms:.0f}</code> ms.")


# --- Bot management ---------------------------------------------------------

@router.message(cmd("modules"), HasRole(Role.DEVELOPER))
async def cmd_modules(message: Message) -> None:
    modules = registry.all_modules()
    if not modules:
        await reply(message, "No modules loaded.")
        return

    lines = ["<b>Modules</b>"]
    for info in modules:
        status = "enabled" if info.enabled else "disabled"
        line = f"<code>{esc(info.name)}</code> - {status}"
        if info.protected:
            line += " [protected]"
        if info.description:
            line += f" ({esc(info.description)})"
        lines.append(line)

    await reply(message, "\n".join(lines))


@router.message(cmd("module"), HasRole(Role.DEVELOPER))
async def cmd_module(message: Message, command: CommandObject) -> None:
    args = (command.args or "").split()
    action = args[0].lower() if args else ""

    if len(args) != 2 or action not in ("enable", "disable"):
        await reply(message, "Usage: /module &lt;enable|disable&gt; &lt;name&gt;")
        return

    name = args[1]
    try:
        found = await registry.set_enabled(name, action == "enable")
    except registry.ProtectedModuleError:
        await reply(message, f"Module <code>{esc(name.lower())}</code> is protected and cannot be disabled.")
        return

    if not found:
        await reply(message, f"Module <code>{esc(name)}</code> not found.")
        return

    past = "enabled" if action == "enable" else "disabled"
    await reply(message, f"Module <code>{esc(name.lower())}</code> {past}.")


@router.message(cmd("cleanchats"), HasRole(Role.DEVELOPER))
async def cmd_cleanchats(message: Message) -> None:
    """Check every stored chat and forget the ones the bot is no longer in."""
    if chats.is_cleaning():
        await reply(message, "A chat cleanup is already running.")
        return

    sent = await reply(message, "Checking chats, this can take a while...")
    result = await chats.cleanup(message.bot)
    if result is None:  # someone else started one in the meantime
        await edit(sent, "A chat cleanup is already running.")
        return
    await edit(sent, chats.report(result, "Chat cleanup finished"))


@router.message(cmd("stats"), HasRole(Role.DEVELOPER))
async def cmd_stats(message: Message) -> None:
    """What the bot has recorded, in numbers."""
    people = await database.fetch_one("SELECT count(*) AS total, count(*) FILTER (WHERE is_bot) AS bots FROM users")
    places = await database.fetch_all("SELECT type, count(*) AS total FROM chats GROUP BY type")
    by_type = {row["type"]: row["total"] for row in places}
    groups = by_type.get("group", 0) + by_type.get("supergroup", 0)
    staff = await database.fetch_one("SELECT count(*) AS total FROM roles")
    approved = await database.fetch_one("SELECT count(*) AS total FROM approvals")
    feds = await database.fetch_one("SELECT count(*) AS total FROM federations")

    lines = [
        "<b>Bot Statistics:</b>",
        "",
        f"• <b>Known Users:</b> <code>{people['total']}</code> (bots: <code>{people['bots']}</code>)",
        f"• <b>Chats:</b> <code>{sum(by_type.values())}</code> "
        f"(groups: <code>{groups}</code>, channels: <code>{by_type.get('channel', 0)}</code>)",
        f"• <b>Global Bans:</b> <code>{await gbans.count()}</code>",
        f"• <b>Federations:</b> <code>{feds['total']}</code>",
        f"• <b>Whitelisted:</b> <code>{len(await whitelist.list_all())}</code>",
        f"• <b>Approved Users:</b> <code>{approved['total']}</code>",
        f"• <b>Staff:</b> <code>{staff['total'] + 1}</code> (including the owner)",
        f"• <b>Modules:</b> <code>{registry.count_enabled()}</code> of <code>{registry.count_loaded()}</code> enabled",
    ]
    await reply(message, "\n".join(lines))


@router.message(cmd("leave"), HasRole(Role.DEVELOPER))
async def cmd_leave(message: Message) -> None:
    """Leave the chat the command is used in."""
    if message.chat.type == "private":
        await reply(message, "I can only leave groups and channels.")
        return

    chat_id = message.chat.id
    await reply(message, "Farewell! My duties here are finished. 🫡")
    try:
        await message.bot.leave_chat(chat_id)
    except TelegramAPIError as exc:
        await reply(message, f"I couldn't leave this chat: <code>{esc(getattr(exc, 'message', exc))}</code>")
        return
    await chats.remove_chat(chat_id)


_speedtest_running = False


@router.message(cmd("speedtest"), HasRole(Role.DEVELOPER))
async def cmd_speedtest(message: Message) -> None:
    global _speedtest_running
    if _speedtest_running:
        await reply(message, "A speedtest is already running.")
        return

    _speedtest_running = True
    sent = await reply(message, "Running a speedtest, this takes a few seconds...")
    try:
        ping, download, upload = await speedtest.run()
        text = (
            "<b>Speedtest</b>\n\n"
            f"• <b>Ping:</b> <code>{ping:.1f} ms</code>\n"
            f"• <b>Download:</b> <code>{download:.2f} Mbps</code>\n"
            f"• <b>Upload:</b> <code>{upload:.2f} Mbps</code>"
        )
    except Exception as exc:  # network trouble of any kind: report it, don't crash
        text = f"<b>Speedtest failed:</b> <code>{esc(str(exc) or type(exc).__name__)}</code>"
    finally:
        _speedtest_running = False
    await edit(sent, text)


# --- Staff roles ------------------------------------------------------------

# Shared with the other modules that write to the user log (see core/logs.py).
_timestamp = logs.timestamp
_role_log_text = logs.audit_text


@router.message(cmd(*GRANT_COMMANDS, *REVOKE_COMMANDS), HasRole(Role.SUPPORT))
async def cmd_staff_role(message: Message, command: CommandObject, role: Role | None = None) -> None:
    granting = command.command in GRANT_COMMANDS
    target_role = (GRANT_COMMANDS if granting else REVOKE_COMMANDS)[command.command]
    actor = message.from_user

    if not roles.can_manage(role, target_role):
        await reply(message, f"You don't have permission to manage {roles.label(target_role, plural=True)}.")
        return

    try:
        target = await resolve_target(message, command)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    if target.user_id == actor.id:
        await reply(message, "You can't change your own role.")
        return
    if target.user_id == settings.OWNER_ID:
        await reply(message, "The owner's role can't be changed.")
        return

    who = user_link(target.user_id, target.name)
    current = await roles.get_role(target.user_id)
    label = roles.label(target_role)

    if granting:
        if current is target_role:
            await reply(message, f"{who} is already {label}.")
            return
        if current is not None:
            await reply(
                message,
                f"{who} is already {roles.label(current)}. Remove that role first.",
            )
            return
        if await gbans.is_gbanned(target.user_id):
            await reply(message, f"{who} is globally banned and can't be promoted.")
            return
        await roles.set_role(target.user_id, target_role, actor.id, target.name, actor.full_name)
        await reply(message, f"{who} has been promoted to <b>{label}</b>.")
    else:
        if current is not target_role:
            await reply(message, f"{who} is not {label}.")
            return
        await roles.remove_role(target.user_id)
        await reply(message, f"{who} has been removed from <b>{label}</b>.")

    granted_tag, revoked_tag = roles.LOG_TAGS[target_role]
    await logs.role_log(
        message.bot,
        _role_log_text(granted_tag if granting else revoked_tag, target.user_id, target.name, actor),
    )


def _staff_home_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=title, callback_data=f"{CB_STAFF_ROLE}{int(role)}")
        for role, title in STAFF_BUTTONS
    ]
    return InlineKeyboardMarkup(inline_keyboard=[buttons[:2], buttons[2:]])


def _staff_back_keyboard() -> InlineKeyboardMarkup:
    return InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text="Back", callback_data=CB_STAFF_HOME)]]
    )


def _staff_home_text() -> str:
    return "<b>Staff</b>\n\nChoose a role to see who holds it."


async def _staff_list_text(bot, role: Role) -> str:
    title = dict(STAFF_BUTTONS)[role]

    if role is Role.OWNER:
        rows = [{"user_id": settings.OWNER_ID, "name": None, "added_at": None, "added_by": None, "added_by_name": None}]
    else:
        rows = await roles.list_role(role)

    if not rows:
        return f"<b>{title}</b>\n\nNobody holds this role yet."

    # Look every person up once, in parallel; fall back to the stored name.
    stored: dict[int, str | None] = {}
    for row in rows:
        stored.setdefault(row["user_id"], row["name"])
        if row["added_by"] is not None:
            stored.setdefault(row["added_by"], row["added_by_name"])
    ids = list(stored)
    found = await asyncio.gather(*(display_name(bot, uid, stored[uid]) for uid in ids))
    names = dict(zip(ids, found))

    lines = [f"<b>{title}</b> ({len(rows)})"]
    shown = 0
    for number, row in enumerate(rows, start=1):
        entry = [f"{number}. {user_ref(row['user_id'], names[row['user_id']])}"]
        if row["added_at"] is not None:
            entry.append(f"   Added: {_timestamp(row['added_at'])}")
        if row["added_by"] is not None:
            entry.append(f"   By: {user_ref(row['added_by'], names.get(row['added_by']))}")
        block = "\n".join(entry)

        if sum(len(line) + 1 for line in lines) + len(block) > STAFF_TEXT_LIMIT:
            lines.append(f"\n...and {len(rows) - shown} more.")
            break
        lines.append("\n" + block)
        shown += 1

    return "\n".join(lines)


@router.message(cmd("staff"), HasRole(Role.SUPPORT))
async def cmd_staff(message: Message) -> None:
    await reply(message, _staff_home_text(), reply_markup=_staff_home_keyboard())


@router.callback_query(F.data == CB_STAFF_HOME, HasRole(Role.SUPPORT))
async def cb_staff_home(callback: CallbackQuery) -> None:
    await callback.answer()
    if isinstance(callback.message, Message):
        await edit(callback.message, _staff_home_text(), reply_markup=_staff_home_keyboard())


@router.callback_query(F.data.startswith(CB_STAFF_ROLE), HasRole(Role.SUPPORT))
async def cb_staff_role(callback: CallbackQuery) -> None:
    try:
        role = Role(int((callback.data or "")[len(CB_STAFF_ROLE):]))
    except ValueError:
        await callback.answer("Unknown role.", show_alert=True)
        return

    await callback.answer()
    if isinstance(callback.message, Message):
        text = await _staff_list_text(callback.bot, role)
        await edit(callback.message, text, reply_markup=_staff_back_keyboard())


# --- Whitelist ------------------------------------------------------------------

# Sudo users and up (Developers, the Owner) manage the list.
WHITELIST_ROLE = Role.SUDO


@router.message(cmd("whitelist", "unwhitelist"), HasRole(WHITELIST_ROLE))
async def cmd_whitelist(message: Message, command: CommandObject) -> None:
    adding = command.command == "whitelist"
    actor = message.from_user

    try:
        target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    if target.user_id == actor.id:
        await reply(message, "You can't change your own whitelist status.")
        return

    who = user_link(target.user_id, target.name)
    if immunity.is_immune(target.user_id):
        await reply(message, f"{who} is immune to all bot actions and doesn't need to be whitelisted.")
        return

    if adding:
        if not await whitelist.add(target.user_id, actor.id, target.name, actor.full_name):
            await reply(message, f"{who} is already whitelisted.")
            return
        tag = "#WHITELIST"
        await reply(message, f"{who} has been whitelisted.")
    else:
        if not await whitelist.remove(target.user_id):
            await reply(message, f"{who} is not whitelisted.")
            return
        tag = "#UNWHITELIST"
        await reply(message, f"{who} has been removed from the whitelist.")

    await logs.role_log(message.bot, logs.audit_text(tag, target.user_id, target.name, actor))


async def _info_flags(bot, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    return [("Immune", immunity.is_immune(user_id)), ("Whitelisted", await whitelist.is_whitelisted(user_id))]


info_hooks.register("root", _info_flags)
