"""Warn commands for chat admins"""

from contextlib import suppress

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import database, moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc, truncate, user_link

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = """
Warns are strikes. Each one is saved with its reason, and a user who reaches the chat's warn limit (3 by default) is banned; the count then starts again from zero.

These commands are for chat admins with the right to ban users. My staff (Sudo and up) can use them too, unless the chat owner turned that off with /staffhelp.

Name the target by replying to their message, or with a @username or a numeric ID. Anything after it is the reason, which is shown in the answer.

I won't act against chat admins, my own staff or protected accounts, and I need the right to restrict users myself.

Every warn has an ID. A fresh warning comes with a button to undo it, and /warns lists a user's warns with buttons to remove each one.
"""

HELP_WHO = "admins"

HELP = [
    ("/warn <user> [reason]", "Give a user a warn. Reaching the limit bans them."),
    ("/warns <user>", "List a user's warns with their reasons and who gave them, with a button to remove each."),
    ("/rmwarn <id>", "Remove one warn by its ID."),
    ("/resetwarns <user>", "Remove every warn of a user."),
    ("/warnlimit", "Show how many warns get a user banned in this chat."),
    ("/setwarnlimit <1-10>", "Set the warn limit of this chat. Default: 3."),
]

router = Router(name="warns")

DEFAULT_LIMIT = 3
MAX_LIMIT = 10

CB_UNDO = "warn:undo:"  # the button under a fresh warning
CB_DELETE = "warn:del:"  # the buttons in a /warns list


# --- Storage ------------------------------------------------------------------

async def get_limit(chat_id: int) -> int:
    row = await database.fetch_one("SELECT warn_limit FROM warn_settings WHERE chat_id = $1", chat_id)
    return row["warn_limit"] if row else DEFAULT_LIMIT


async def set_limit(chat_id: int, limit: int) -> None:
    await database.execute(
        "INSERT INTO warn_settings (chat_id, warn_limit) VALUES ($1, $2) "
        "ON CONFLICT (chat_id) DO UPDATE SET warn_limit = EXCLUDED.warn_limit",
        chat_id,
        limit,
    )


async def add_warn(chat_id: int, target, reason: str, actor: moderation.Actor) -> int:
    row = await database.fetch_one(
        "INSERT INTO warns (chat_id, user_id, user_name, reason, warned_by, warned_by_name, warned_by_staff) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id",
        chat_id,
        target.user_id,
        target.name,
        reason or None,
        actor.id,
        actor.name,
        actor.staff,
    )
    return row["id"]


async def list_warns(chat_id: int, user_id: int) -> list[dict]:
    return await database.fetch_all(
        "SELECT id, user_id, user_name, reason, warned_by_name, warned_by_staff, created_at "
        "FROM warns WHERE chat_id = $1 AND user_id = $2 ORDER BY id",
        chat_id,
        user_id,
    )


async def delete_warn(chat_id: int, warn_id: int) -> dict | None:
    """Remove one warn of this chat. None if there is no such warn here."""
    return await database.fetch_one(
        "DELETE FROM warns WHERE id = $1 AND chat_id = $2 RETURNING id, user_id, user_name",
        warn_id,
        chat_id,
    )


async def clear_warns(chat_id: int, user_id: int) -> int:
    rows = await database.fetch_all(
        "DELETE FROM warns WHERE chat_id = $1 AND user_id = $2 RETURNING id", chat_id, user_id
    )
    return len(rows)


# --- Wording ------------------------------------------------------------------

def _undo_keyboard(warn_id: int) -> InlineKeyboardMarkup:
    button = InlineKeyboardButton(text="Remove warn", callback_data=f"{CB_UNDO}{warn_id}")
    return InlineKeyboardMarkup(inline_keyboard=[[button]])


def _warns_view(who: str, warns: list[dict], limit: int) -> tuple[str, InlineKeyboardMarkup | None]:
    if not warns:
        return f"{who} has no warns.", None

    lines = [f"{who} has {len(warns)}/{limit} warns.", ""]
    for warn in warns:
        reason = esc(truncate(warn["reason"], 100)) if warn["reason"] else "No reason given"
        lines.append(f"<code>#{warn['id']}</code> {reason}")
        by = esc(warn["warned_by_name"] or "Unknown")
        if warn["warned_by_staff"]:
            by += " (bot staff)"
        lines.append(f"By {by} on {warn['created_at']:%Y-%m-%d}")

    buttons = [
        InlineKeyboardButton(text=f"Remove #{warn['id']}", callback_data=f"{CB_DELETE}{warn['id']}")
        for warn in warns
    ]
    rows = [buttons[i : i + 2] for i in range(0, len(buttons), 2)]
    return "\n".join(lines), InlineKeyboardMarkup(inline_keyboard=rows)


# --- /warn --------------------------------------------------------------------

@router.message(cmd("warn"))
async def cmd_warn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role)
    if started is None:
        return
    target, actor = started

    chat_id = message.chat.id
    reason = target.extra.strip()
    warn_id = await add_warn(chat_id, target, reason, actor)
    count = len(await list_warns(chat_id, target.user_id))
    limit = await get_limit(chat_id)
    who = moderation.who(target)
    text = f"{who} has been warned ({count}/{limit}).{moderation.reason_line(reason)}{moderation.attribution(actor)}"

    if count < limit:
        await reply(message, text, reply_markup=_undo_keyboard(warn_id))
        return

    # The limit is reached: ban, and start them from zero if they ever come back.
    try:
        await perform(message.bot.ban_chat_member(chat_id, target.user_id))
    except ModerationError as exc:
        # The warns stay, so the situation is not lost; the button still works.
        await reply(
            message,
            f"{text}\nThe limit is reached, but I couldn't ban them: {esc(exc)}",
            reply_markup=_undo_keyboard(warn_id),
        )
        return

    await clear_warns(chat_id, target.user_id)
    await reply(
        message,
        f"{who} has reached the warn limit ({count}/{limit}) and has been banned."
        f"{moderation.reason_line(reason)}{moderation.attribution(actor)}",
    )


# --- /warns and the buttons ---------------------------------------------------

@router.message(cmd("warns"))
async def cmd_warns(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role, punitive=False)
    if started is None:
        return
    target, _ = started

    warns = await list_warns(message.chat.id, target.user_id)
    text, keyboard = _warns_view(moderation.who(target), warns, await get_limit(message.chat.id))
    await reply(message, text, reply_markup=keyboard)


async def _authorize_callback(
    callback: CallbackQuery, role: Role | None
) -> tuple[Message, moderation.Actor] | None:
    """The message a button is under and who pressed it, if they may moderate there."""
    message = callback.message
    if not isinstance(message, Message):
        await callback.answer("This message is too old.", show_alert=True)
        return None
    try:
        capacity = await moderation.authorize(callback.bot, message.chat, callback.from_user, role)
    except ModerationError as exc:
        await callback.answer(str(exc), show_alert=True)
        return None
    user = callback.from_user
    return message, moderation.Actor(user.id, user.full_name, staff=capacity == "staff")


def _warn_id(callback: CallbackQuery, prefix: str) -> int | None:
    value = (callback.data or "")[len(prefix):]
    return int(value) if value.isdigit() else None


@router.callback_query(F.data.startswith(CB_UNDO))
async def cb_undo(callback: CallbackQuery, role: Role | None = None) -> None:
    warn_id = _warn_id(callback, CB_UNDO)
    allowed = await _authorize_callback(callback, role)
    if allowed is None:
        return
    message, actor = allowed
    if warn_id is None:
        await callback.answer("This button is not valid.", show_alert=True)
        return

    removed = await delete_warn(message.chat.id, warn_id)
    if removed is None:
        await callback.answer("This warn no longer exists.", show_alert=True)
        with suppress(TelegramAPIError):
            await message.edit_reply_markup(reply_markup=None)
        return

    await callback.answer()
    who = user_link(removed["user_id"], removed["user_name"])
    await edit(
        message,
        f"The warn <code>#{removed['id']}</code> of {who} has been removed.{moderation.attribution(actor)}",
        reply_markup=None,
    )


@router.callback_query(F.data.startswith(CB_DELETE))
async def cb_delete(callback: CallbackQuery, role: Role | None = None) -> None:
    warn_id = _warn_id(callback, CB_DELETE)
    allowed = await _authorize_callback(callback, role)
    if allowed is None:
        return
    message, _actor = allowed
    if warn_id is None:
        await callback.answer("This button is not valid.", show_alert=True)
        return

    removed = await delete_warn(message.chat.id, warn_id)
    if removed is None:
        await callback.answer("This warn no longer exists.", show_alert=True)
        return

    await callback.answer(f"Warn #{removed['id']} removed.")
    who = user_link(removed["user_id"], removed["user_name"])
    warns = await list_warns(message.chat.id, removed["user_id"])
    text, keyboard = _warns_view(who, warns, await get_limit(message.chat.id))
    await edit(message, text, reply_markup=keyboard)


# --- /rmwarn, /resetwarns -----------------------------------------------------

@router.message(cmd("rmwarn"))
async def cmd_rmwarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role)
    if actor is None:
        return

    argument = (command.args or "").strip().lstrip("#")
    if not argument.isdigit():
        await reply(message, "Give the ID of the warn, for example: /rmwarn 12")
        return

    removed = await delete_warn(message.chat.id, int(argument))
    if removed is None:
        await reply(message, "There is no warn with that ID in this chat.")
        return
    who = user_link(removed["user_id"], removed["user_name"])
    await reply(
        message,
        f"The warn <code>#{removed['id']}</code> of {who} has been removed.{moderation.attribution(actor)}",
    )


@router.message(cmd("resetwarns"))
async def cmd_resetwarns(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role, punitive=False)
    if started is None:
        return
    target, actor = started

    removed = await clear_warns(message.chat.id, target.user_id)
    who = moderation.who(target)
    if removed == 0:
        await reply(message, f"{who} has no warns.")
    else:
        await reply(message, f"All warns of {who} have been removed ({removed}).{moderation.attribution(actor)}")


# --- Limit --------------------------------------------------------------------

@router.message(cmd("warnlimit"))
async def cmd_warnlimit(message: Message, command: CommandObject, role: Role | None = None) -> None:
    # Chat settings are for the chat's own admins; the bot's staff stays out of them.
    if await moderation.begin_chat_command(message, role, allow_staff=False) is None:
        return
    limit = await get_limit(message.chat.id)
    await reply(message, f"The warn limit in this chat is <b>{limit}</b>.")


@router.message(cmd("setwarnlimit"))
async def cmd_setwarnlimit(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role, allow_staff=False) is None:
        return

    argument = (command.args or "").strip()
    if not argument.isdigit() or not 1 <= int(argument) <= MAX_LIMIT:
        await reply(message, f"Give a number from 1 to {MAX_LIMIT}, for example: /setwarnlimit 5")
        return

    await set_limit(message.chat.id, int(argument))
    await reply(message, f"The warn limit in this chat is now <b>{int(argument)}</b>.")
