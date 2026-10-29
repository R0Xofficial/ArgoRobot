"""Core bot commands"""

import asyncio
import time
from datetime import datetime, timezone

from aiogram import F, Router
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from config import settings
from core import chats, logs, registry, roles
from core.filters import HasRole, cmd
from core.helpers import edit, reply
from core.roles import Role
from core.targets import TargetError, display_name, resolve_target
from core.text import esc, user_link, user_ref

# Protected modules cannot be disabled with /module, so the commands in this
# file (including /module itself) are always available.
PROTECTED = True

# Commands shown in /help (see core/help_index.py).
HELP = [
    ("/ping", "Check how fast I respond"),
    ("/modules", "List modules and their status", Role.DEVELOPER),
    ("/module <enable|disable> <name>", "Enable or disable a module", Role.DEVELOPER),
    ("/cleanchats", "Forget chats I have been removed from", Role.DEVELOPER),
    ("/staff", "Show who holds each staff role", Role.SUPPORT),
    ("/adddev <user>", "Grant the Developer role", Role.OWNER),
    ("/rmdev <user>", "Remove the Developer role", Role.OWNER),
    ("/addsudo <user>", "Grant the Sudo role", Role.DEVELOPER),
    ("/rmsudo <user>", "Remove the Sudo role", Role.DEVELOPER),
    ("/addsupport <user>", "Grant the Support role", Role.SUDO),
    ("/rmsupport <user>", "Remove the Support role", Role.SUDO),
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


# --- Staff roles ------------------------------------------------------------

def _timestamp(when: datetime | None = None) -> str:
    when = (when or datetime.now(timezone.utc)).astimezone(timezone.utc)
    return f"<code>{when:%Y-%m-%d %H:%M:%S} UTC</code>"


def _role_log_text(tag: str, user_id: int, name: str | None, admin) -> str:
    return (
        f"<b>{tag}</b>\n"
        f"<b>User:</b> {user_ref(user_id, name)}\n"
        f"<b>Timestamp:</b> {_timestamp()}\n"
        f"<b>Admin:</b> {user_ref(admin.id, admin.full_name)}"
    )


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
