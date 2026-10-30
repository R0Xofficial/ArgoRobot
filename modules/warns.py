"""Warn commands for chat admins"""

from contextlib import suppress

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import blacklist, database, moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc, parse_switch, truncate, user_link

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = """
Warns are strikes. Each one is saved with its reason, and a user who reaches the chat's warn limit (3 by default) is banned, or kicked if /strongwarn is off; the count then starts again from zero.

These commands are for chat admins with the right to ban users. My staff (Sudo and up) can use them too, unless the chat owner turned that off with /staffhelp.

Name the target by replying to their message, or with a @username or a numeric ID. Anything after it is the reason, which is shown in the answer.

I won't act against chat admins, my own staff or protected accounts, and I need the right to restrict users myself.

Every warn has an ID. A fresh warning comes with a button to undo it, and /warns lists a user's warns with buttons to remove each one.

Warn filters give a warn to whoever writes a word or phrase: /addwarn spam "free money" and the reason. They are blacklist triggers with the warn action, so they follow the blacklist rules (wildcards, whether the message is deleted).
"""

HELP_WHO = "admins"

HELP = [
    ("/warn <user> [reason]", "Give a user a warn. Reaching the limit bans them (or kicks them, see /strongwarn)."),
    ("/dwarn <reply> [reason]", "Reply to a message: warn its author and delete that message."),
    ("/unwarn <user>", "Remove the latest warn of a user."),
    ("/warns <user>", "List a user's warns with their reasons and who gave them, with a button to remove each."),
    ("/rmwarn <id>", "Remove one warn by its ID."),
    ("/resetwarns <user>", "Remove every warn of a user. Alias: /resetwarn."),
    ("/warnlimit", "Show how many warns get a user banned in this chat."),
    ("/setwarnlimit <1-10>", "Set the warn limit of this chat. Default: 3."),
    ("/strongwarn [on|off]", "On (default): reaching the limit bans. Off: it only kicks, and the user can come back. Without an answer it shows the setting."),
    ("/addwarn <word or \"phrase\"> [reason]", "Warn everyone who writes that word or phrase. The reason can also come from a message you reply to."),
    ("/nowarn <word or \"phrase\">", "Remove a warn filter. Alias: /stopwarn."),
    ("/warnlist", "List the warn filters of this chat."),
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


async def get_strong(chat_id: int) -> bool:
    row = await database.fetch_one("SELECT strong FROM warn_settings WHERE chat_id = $1", chat_id)
    return row["strong"] if row else True


async def set_strong(chat_id: int, strong: bool) -> None:
    await database.execute(
        "INSERT INTO warn_settings (chat_id, warn_limit, strong) VALUES ($1, $2, $3) "
        "ON CONFLICT (chat_id) DO UPDATE SET strong = EXCLUDED.strong",
        chat_id,
        DEFAULT_LIMIT,
        strong,
    )


async def limit_action(bot, chat_id: int, user_id: int) -> str:
    """What reaching the warn limit does: ban (strong warns, the default) or kick. Returns "banned" or "kicked"."""
    await perform(bot.ban_chat_member(chat_id, user_id))
    if await get_strong(chat_id):
        return "banned"
    await perform(bot.unban_chat_member(chat_id, user_id, only_if_banned=True))
    return "kicked"


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

@router.message(cmd("warn", "dwarn"))
async def cmd_warn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    deleting = command.command.lower() == "dwarn"
    started = await moderation.begin(message, command, role)
    if started is None:
        return
    target, actor = started

    replied = await moderation.require_reply(message) if deleting else None
    if deleting and replied is None:
        return

    chat_id = message.chat.id
    reason = target.extra.strip()
    warn_id = await add_warn(chat_id, target, reason, actor)
    if deleting:
        # Best effort: the message may already be gone.
        with suppress(TelegramAPIError):
            await message.bot.delete_message(chat_id, replied.message_id)
    count = len(await list_warns(chat_id, target.user_id))
    limit = await get_limit(chat_id)
    who = moderation.who(target)
    await moderation.log_action(message, "WARNED", actor, target, reason, ("Warns", f"<code>{count}/{limit}</code>"))
    text = f"{who} was warned ({count}/{limit}){moderation.by(actor)}{moderation.reason_line(reason)}{moderation.staff_note(actor)}"

    if count < limit:
        await reply(message, text, reply_markup=_undo_keyboard(warn_id))
        return

    # The limit is reached: ban (or kick), and start them from zero if they ever come back.
    try:
        done = await limit_action(message.bot, chat_id, target.user_id)
    except ModerationError as exc:
        # The warns stay, so the situation is not lost; the button still works.
        await reply(
            message,
            f"{text}\nThe limit is reached, but I couldn't remove them: {esc(exc)}",
            reply_markup=_undo_keyboard(warn_id),
        )
        return

    await clear_warns(chat_id, target.user_id)
    await moderation.log_action(message, "WARNLIMIT", actor, target, reason, ("Result", done))
    await reply(
        message,
        f"{who} reached the warn limit ({count}/{limit}) and was {done}{moderation.by(actor)}"
        f"{moderation.reason_line(reason)}{moderation.staff_note(actor)}",
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
        f"The warn <code>#{removed['id']}</code> of {who} was removed{moderation.by(actor)}{moderation.staff_note(actor)}",
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
        f"The warn <code>#{removed['id']}</code> of {who} was removed{moderation.by(actor)}{moderation.staff_note(actor)}",
    )


@router.message(cmd("resetwarns", "resetwarn"))
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
        await moderation.log_action(message, "WARNSRESET", actor, target, "", ("Removed", f"<code>{removed}</code>"))
        await reply(message, f"All warns of {who} were removed ({removed}){moderation.by(actor)}{moderation.staff_note(actor)}")


# --- /unwarn ------------------------------------------------------------------

@router.message(cmd("unwarn"))
async def cmd_unwarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    started = await moderation.begin(message, command, role, punitive=False)
    if started is None:
        return
    target, actor = started

    warns = await list_warns(message.chat.id, target.user_id)
    who = moderation.who(target)
    if not warns:
        await reply(message, f"{who} has no warns.")
        return
    await delete_warn(message.chat.id, warns[-1]["id"])
    limit = await get_limit(message.chat.id)
    await moderation.log_action(message, "UNWARNED", actor, target, "", ("Warns", f"<code>{len(warns) - 1}/{limit}</code>"))
    await reply(
        message,
        f"The latest warn of {who} was removed{moderation.by(actor)} ({len(warns) - 1}/{limit} left)"
        f"{moderation.staff_note(actor)}",
    )


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


@router.message(cmd("strongwarn"))
async def cmd_strongwarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role, allow_staff=False) is None:
        return

    argument = (command.args or "").strip()
    chat_id = message.chat.id
    if argument:
        wanted = parse_switch(argument)
        if wanted is None:
            await reply(message, "Use /strongwarn on or /strongwarn off.")
            return
        if await get_strong(chat_id) == wanted:
            await reply(message, f"Strong warns are already <b>{'on' if wanted else 'off'}</b>.")
            return
        await set_strong(chat_id, wanted)
    strong = await get_strong(chat_id)
    await reply(
        message,
        f"Strong warns are <b>{'on' if strong else 'off'}</b>: a user who reaches the warn limit is "
        + ("banned." if strong else "kicked, and can come back."),
    )


# --- Warn filters -----------------------------------------------------------------
#
# A warn filter is a blacklist trigger whose action is "warn" (see core/blacklist.py): the
# blacklist already matches every message, warns the sender and counts toward the limit.

_QUOTES = "\"'“”‘’"


def _split_keyword(text: str) -> tuple[str, str]:
    """'spam why' -> ('spam', 'why'); '"free money" why' -> ('free money', 'why')."""
    text = text.strip()
    if text and text[0] in _QUOTES:
        end = next((i for i in range(1, len(text)) if text[i] in _QUOTES), None)
        if end is not None:
            return text[1:end].strip(), text[end + 1 :].strip()
        return text[1:].strip(), ""
    keyword, _, rest = text.partition(" ")
    return keyword, rest.strip()


async def _warn_triggers(chat_id: int):
    return [t for t in await blacklist.triggers(chat_id) if t.action == "warn"]


@router.message(cmd("addwarn"))
async def cmd_addwarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, allow_staff=False)
    if actor is None:
        return

    keyword, reason = _split_keyword(command.args or "")
    replied = moderation.replied_message(message)
    if not reason and replied is not None:
        reason = " ".join((replied.text or replied.caption or "").split())
    if not keyword:
        await reply(message, "Give a word or a phrase in quotes, then a reason: /addwarn spam \"No advertising here\"")
        return
    try:
        pattern = blacklist.check(keyword)
        outcome = await blacklist.add(message.chat.id, pattern, "warn", 0, truncate(reason, 200) or None, actor.id)
    except blacklist.TriggerError as exc:
        await reply(message, esc(exc))
        return
    if outcome is None:
        await reply(message, f"The blacklist is full ({blacklist.MAX_TRIGGERS} triggers).")
        return
    await reply(
        message,
        f"Anyone who writes <code>{esc(pattern)}</code> gets a warn. The filter was {'added' if outcome else 'updated'}{moderation.by(actor)}",
    )


@router.message(cmd("nowarn", "stopwarn"))
async def cmd_nowarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, allow_staff=False)
    if actor is None:
        return

    keyword, _ = _split_keyword(command.args or "")
    if not keyword:
        await reply(message, "Give the word or phrase of the filter: /nowarn spam")
        return
    try:
        pattern = blacklist.check(keyword)
    except blacklist.TriggerError as exc:
        await reply(message, esc(exc))
        return
    if not any(t.pattern == pattern for t in await _warn_triggers(message.chat.id)) or not await blacklist.remove(message.chat.id, pattern):
        await reply(message, "There is no warn filter like that in this chat. /warnlist shows them.")
        return
    await reply(message, f"The warn filter <code>{esc(pattern)}</code> was removed{moderation.by(actor)}")


@router.message(cmd("warnlist"))
async def cmd_warnlist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role, allow_staff=False) is None:
        return
    found = await _warn_triggers(message.chat.id)
    if not found:
        await reply(message, "There are no warn filters in this chat. Add one with /addwarn.")
        return
    lines = ["<b>Warn filters</b>"]
    for trigger in found[:80]:
        line = f"• <code>{esc(trigger.pattern)}</code>"
        if trigger.reason:
            line += f" - {esc(truncate(trigger.reason, 80))}"
        lines.append(line)
    if len(found) > 80:
        lines.append(f"…and {len(found) - 80} more.")
    await reply(message, "\n".join(lines))
