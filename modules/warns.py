"""Warn commands for chat admins"""

from contextlib import suppress

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import blacklist, database, enforce, moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import duration_text, esc, parse_switch, truncate, user_link
from core.i18n import tr, word

# Usable by chat admins (with the right to ban users) and by the bot's Owner,
# Developers and Sudo users. See core/moderation.py.
ABOUT = "warns.about"

HELP_WHO = "common.admins"

HELP = [
    ("/warn <user> [reason]", "warns.help.give_a_user_a_warn"),
    ("/dwarn <reply> [reason]", "warns.help.reply_to_a_message_warn"),
    ("/unwarn <user>", "warns.help.remove_the_latest_warn_of"),
    ("/warns <user>", "warns.help.list_a_user_s_warns"),
    ("/rmwarn <id>", "warns.help.remove_one_warn_by_its"),
    ("/resetwarns <user>", "warns.help.remove_every_warn_of_a"),
    ("/warnlimit", "warns.help.show_how_many_warns_it"),
    ("/setwarnlimit <1-10>", "warns.help.set_the_warn_limit_of"),
    ("/warnmode <ban|kick|mute|tban|tmute> [time]", "warns.help.what_reaching_the_warn_limit"),
    ("/strongwarn [on|off]", "warns.help.the_short_form_of_warnmode"),
    ("/addwarn <word or \"phrase\"> [reason]", "warns.help.warn_everyone_who_writes_that"),
    ("/nowarn <word or \"phrase\">", "warns.help.remove_a_warn_filter_alias"),
    ("/warnlist", "warns.help.list_the_warn_filters_of"),
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


async def get_mode(chat_id: int) -> tuple[str, int]:
    """What reaching the warn limit does: (mode, seconds) with mode ban (default), kick, mute, tban or tmute."""
    row = await database.fetch_one("SELECT limit_mode, limit_seconds FROM warn_settings WHERE chat_id = $1", chat_id)
    return (row["limit_mode"], row["limit_seconds"]) if row else ("ban", 0)


async def set_mode(chat_id: int, mode: str, seconds: int = 0) -> None:
    await database.execute(
        "INSERT INTO warn_settings (chat_id, warn_limit, strong, limit_mode, limit_seconds) VALUES ($1, $2, $3, $4, $5) "
        "ON CONFLICT (chat_id) DO UPDATE SET strong = EXCLUDED.strong, limit_mode = EXCLUDED.limit_mode, "
        "limit_seconds = EXCLUDED.limit_seconds",
        chat_id,
        DEFAULT_LIMIT,
        mode != "kick",
        mode,
        seconds,
    )


def mode_text(mode: str, seconds: int) -> str:
    """'ban', 'tmute for 2 hours'."""
    return tr("common.for", mode=word(mode), duration_text=duration_text(seconds)) if mode in enforce.TIMED_MODES else word(mode)


async def limit_action(bot, chat_id: int, user_id: int) -> str:
    """What reaching the warn limit does, as the chat chose with /warnmode. Returns what was done: 'banned', 'muted for 1 hour'..."""
    mode, seconds = await get_mode(chat_id)
    return await enforce.punish(bot, chat_id, user_id, mode, seconds)


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
    button = InlineKeyboardButton(text=tr("warns.remove_warn"), callback_data=f"{CB_UNDO}{warn_id}")
    return InlineKeyboardMarkup(inline_keyboard=[[button]])


def _warns_view(who: str, warns: list[dict], limit: int) -> tuple[str, InlineKeyboardMarkup | None]:
    if not warns:
        return tr("warns.has_no_warns", who=who), None

    lines = [tr("warns.has_warns", who=who, warns=len(warns), limit=limit), ""]
    for warn in warns:
        reason = esc(truncate(warn["reason"], 100)) if warn["reason"] else tr("warns.no_reason_given")
        lines.append(f"<code>#{warn['id']}</code> {reason}")
        by = esc(warn["warned_by_name"] or tr("common.unknown"))
        if warn["warned_by_staff"]:
            by += tr("warns.bot_staff")
        lines.append(f"By {by} on {warn['created_at']:%Y-%m-%d}")

    buttons = [
        InlineKeyboardButton(text=tr("warns.remove", id=warn['id']), callback_data=f"{CB_DELETE}{warn['id']}")
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
    await moderation.log_action(message, "WARNED", actor, target, reason, (tr("warns.warns"), f"<code>{count}/{limit}</code>"))
    text = tr("warns.was_warned", who=who, count=count, limit=limit, by=moderation.by(actor), reason_line=moderation.reason_line(reason), staff_note=moderation.staff_note(actor))

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
            tr("warns.the_limit_is_reached_but_i", text=text, exc=esc(exc)),
            reply_markup=_undo_keyboard(warn_id),
        )
        return

    await clear_warns(chat_id, target.user_id)
    await moderation.log_action(message, "WARNLIMIT", actor, target, reason, (tr("warns.result"), done))
    await reply(
        message,
        tr("warns.reached_the_warn_limit_and_was", who=who, count=count, limit=limit, done=done, by=moderation.by(actor), reason_line=moderation.reason_line(reason), staff_note=moderation.staff_note(actor)),
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


_authorize_callback = moderation.authorize_callback


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
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return

    removed = await delete_warn(message.chat.id, warn_id)
    if removed is None:
        await callback.answer(tr("warns.this_warn_no_longer_exists"), show_alert=True)
        with suppress(TelegramAPIError):
            await message.edit_reply_markup(reply_markup=None)
        return

    await callback.answer()
    who = user_link(removed["user_id"], removed["user_name"])
    await edit(
        message,
        tr("warns.the_warn_of_was_removed", id=removed['id'], who=who, by=moderation.by(actor), staff_note=moderation.staff_note(actor)),
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
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return

    removed = await delete_warn(message.chat.id, warn_id)
    if removed is None:
        await callback.answer(tr("warns.this_warn_no_longer_exists"), show_alert=True)
        return

    await callback.answer(tr("warns.warn_removed", id=removed['id']))
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
        await reply(message, tr("warns.give_the_id_of_the_warn"))
        return

    removed = await delete_warn(message.chat.id, int(argument))
    if removed is None:
        await reply(message, tr("warns.there_is_no_warn_with_that"))
        return
    who = user_link(removed["user_id"], removed["user_name"])
    await reply(
        message,
        tr("warns.the_warn_of_was_removed", id=removed['id'], who=who, by=moderation.by(actor), staff_note=moderation.staff_note(actor)),
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
        await reply(message, tr("warns.has_no_warns", who=who))
    else:
        await moderation.log_action(message, "WARNSRESET", actor, target, "", (tr("warns.removed"), f"<code>{removed}</code>"))
        await reply(message, tr("warns.all_warns_of_were_removed", who=who, removed=removed, by=moderation.by(actor), staff_note=moderation.staff_note(actor)))


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
        await reply(message, tr("warns.has_no_warns", who=who))
        return
    await delete_warn(message.chat.id, warns[-1]["id"])
    limit = await get_limit(message.chat.id)
    await moderation.log_action(message, "UNWARNED", actor, target, "", (tr("warns.warns"), f"<code>{len(warns) - 1}/{limit}</code>"))
    await reply(
        message,
        tr("warns.the_latest_warn_of_was_removed", who=who, by=moderation.by(actor), v=len(warns) - 1, limit=limit, staff_note=moderation.staff_note(actor)),
    )


# --- Limit --------------------------------------------------------------------

@router.message(cmd("warnlimit"))
async def cmd_warnlimit(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role) is None:
        return
    limit = await get_limit(message.chat.id)
    await reply(message, tr("warns.the_warn_limit_in_this_chat", limit=limit))


@router.message(cmd("setwarnlimit"))
async def cmd_setwarnlimit(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role) is None:
        return

    argument = (command.args or "").strip()
    if not argument.isdigit() or not 1 <= int(argument) <= MAX_LIMIT:
        await reply(message, tr("warns.give_a_number_from_1_to", MAX_LIMIT=MAX_LIMIT))
        return

    await set_limit(message.chat.id, int(argument))
    await reply(message, tr("warns.the_warn_limit_in_this_chat_2", argument=int(argument)))


@router.message(cmd("strongwarn"))
async def cmd_strongwarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """The old switch: on = ban at the limit, off = kick. /warnmode has the rest of the choices."""
    if await moderation.begin_chat_command(message, role) is None:
        return

    argument = (command.args or "").strip()
    chat_id = message.chat.id
    if argument:
        wanted = parse_switch(argument)
        if wanted is None:
            await reply(message, tr("warns.use_strongwarn_on_or_strongwarn_off"))
            return
        await set_mode(chat_id, "ban" if wanted else "kick")
    mode, seconds = await get_mode(chat_id)
    await reply(message, tr("warns.a_user_who_reaches_the_warn", mode_text=esc(mode_text(mode, seconds))))


@router.message(cmd("warnmode"))
async def cmd_warnmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role) is None:
        return

    spec = (command.args or "").strip()
    chat_id = message.chat.id
    if spec:
        try:
            mode, seconds = blacklist.parse_action(spec)
        except blacklist.TriggerError as exc:
            await reply(message, f"{esc(exc)}\n{tr(WARNMODE_USAGE)}")
            return
        if mode not in enforce.MODES:  # delete and warn make no sense at the limit
            await reply(message, tr(WARNMODE_USAGE))
            return
        await set_mode(chat_id, mode, seconds)
        await reply(message, tr("warns.a_user_who_reaches_the_warn_2", mode_text=esc(mode_text(mode, seconds))))
        return
    mode, seconds = await get_mode(chat_id)
    await reply(
        message,
        tr("warns.a_user_who_reaches_the_warn_3", mode_text=esc(mode_text(mode, seconds)), WARNMODE_USAGE=tr(WARNMODE_USAGE)),
    )


WARNMODE_USAGE = (
    "warns.warnmode_usage"
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
    actor = await moderation.begin_chat_command(message, role)
    if actor is None:
        return

    keyword, reason = _split_keyword(command.args or "")
    replied = moderation.replied_message(message)
    if not reason and replied is not None:
        reason = " ".join((replied.text or replied.caption or "").split())
    if not keyword:
        await reply(message, tr("warns.give_a_word_or_a_phrase"))
        return
    try:
        pattern = blacklist.check(keyword)
        outcome = await blacklist.add(message.chat.id, pattern, "warn", 0, truncate(reason, 200) or None, actor.id)
    except blacklist.TriggerError as exc:
        await reply(message, esc(exc))
        return
    if outcome is None:
        await reply(message, tr("common.the_blacklist_is_full_triggers", MAX_TRIGGERS=blacklist.MAX_TRIGGERS))
        return
    await reply(
        message,
        tr("warns.anyone_who_writes_gets_a_warn", pattern=esc(pattern), opt=tr("warns.added") if outcome else tr("warns.updated"), by=moderation.by(actor), staff_note=moderation.staff_note(actor)),
    )


@router.message(cmd("nowarn", "stopwarn"))
async def cmd_nowarn(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role)
    if actor is None:
        return

    keyword, _ = _split_keyword(command.args or "")
    if not keyword:
        await reply(message, tr("warns.give_the_word_or_phrase_of"))
        return
    try:
        pattern = blacklist.check(keyword)
    except blacklist.TriggerError as exc:
        await reply(message, esc(exc))
        return
    if not any(t.pattern == pattern for t in await _warn_triggers(message.chat.id)) or not await blacklist.remove(message.chat.id, pattern):
        await reply(message, tr("warns.there_is_no_warn_filter_like"))
        return
    await reply(message, tr("warns.the_warn_filter_was_removed", pattern=esc(pattern), by=moderation.by(actor), staff_note=moderation.staff_note(actor)))


@router.message(cmd("warnlist"))
async def cmd_warnlist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role) is None:
        return
    found = await _warn_triggers(message.chat.id)
    if not found:
        await reply(message, tr("warns.there_are_no_warn_filters_in"))
        return
    lines = [tr("warns.warn_filters")]
    for trigger in found[:80]:
        line = f"• <code>{esc(trigger.pattern)}</code>"
        if trigger.reason:
            line += f" - {esc(truncate(trigger.reason, 80))}"
        lines.append(line)
    if len(found) > 80:
        lines.append(f"…and {len(found) - 80} more.")
    await reply(message, "\n".join(lines))
