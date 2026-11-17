"""Forbidden words and phrases"""

import re
import time
from contextlib import suppress
from types import SimpleNamespace
from typing import Any

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import approvals, blacklist, enforce, immunity, moderation, roles
from core.blacklist import TriggerError
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, MAX_DURATION, MIN_DURATION, ModerationError, parse_duration, perform
from core.roles import Role
from core.targets import replied_message
from core.text import duration_text, esc, truncate, user_link, SWITCH as _ON_OFF
from core.i18n import tr, word

# Messages that match a trigger are deleted, or get a custom action. Blacklists do not
# affect group admins, or approved users.
ABOUT = "blacklists.about"

HELP = [
    ("/blacklists", "blacklists.help.list_the_blacklisted_triggers_of"),
    ("/addblacklist <reply/triggers>", "blacklists.help.add_one_or_more_triggers", "common.admins"),
    ("/unblacklist <triggers>", "blacklists.help.remove_triggers_one_per_line", "common.admins"),
    ("/blacklistmode", "blacklists.help.show_the_default_action_and", "common.admins"),
    ("/setblacklistmode <off|del|warn|ban|kick|mute|tban|tmute>", "blacklists.help.set_the_default_action_tban", "common.admins"),
    ("/blocklistdelete <on|off>", "blacklists.help.delete_blacklisted_messages_whatever_the", "common.admins"),
    ("/unblacklistall", "blacklists.help.remove_every_trigger_at_once", "common.chat_owner"),
]

HELP_NOTES = [
    (
        "blacklists.note.custom_action_per_trigger",
        "blacklists.note.append_after_a_trigger_to",
    ),
    (
        "common.match_modifiers",
        "blacklists.note.exact_word_match_only_messages",
    ),
    (
        "common.wildcards",
        "blacklists.note.any_single_character_except_spaces",
    ),
    (
        "blacklists.note.stickers",
        "blacklists.note.reply_to_a_sticker_with",
    ),
]

router = Router(name="blacklists")

# After antiflood (-60), before the modules that answer commands: a blacklisted message
# should be gone whatever else it asks for.
PRIORITY = -55

RIGHT = "can_change_info"
CB_CLEAR = "blacklist:clear:"  # blacklist:clear:yes / blacklist:clear:no
FAILURE_COOLDOWN = 300.0
MAX_SHOWN = 80
MAX_SHOWN_ADDED = 25

_ACTION_SUFFIX = re.compile(r"\{([^{}]*)\}\s*$")
_QUOTES = "\"'“”‘’"
_complained: dict[int, float] = {}


# --- Wording and parsing -----------------------------------------------------------------

def _action_name(action: str | None) -> str:
    return "delete" if action == "del" else str(action)


def _mode_text(mode: str, seconds: int) -> str:
    if mode == "off":
        return tr("common.off")
    if mode in enforce.TIMED_MODES:
        return tr("common.for", mode=word(mode), duration_text=duration_text(seconds))
    return word(_action_name(mode))


def _trigger_line(trigger: blacklist.Trigger) -> str:
    line = f"<code>{esc(trigger.pattern)}</code>"
    if trigger.action:
        line += f" {{{esc(_mode_text(trigger.action, trigger.seconds))}}}"
    return line


def split_action(text: str) -> tuple[str, str | None]:
    """'trigger {ban}' -> ('trigger', 'ban')."""
    match = _ACTION_SUFFIX.search(text)
    if match is None:
        return text.strip(), None
    return text[: match.start()].strip(), match.group(1).strip()


def _unquote(text: str) -> str:
    text = text.strip()
    if len(text) >= 2 and text[0] in _QUOTES and text[-1] in _QUOTES:
        return text[1:-1]
    return text


def _sticker_trigger(sticker) -> str:
    if getattr(sticker, "set_name", None):
        return f"stickerpack:{sticker.set_name}"
    if getattr(sticker, "emoji", None):
        return f"sticker:{sticker.emoji}"
    raise TriggerError(tr("blacklists.that_sticker_has_neither_a_pack"))


# --- Commands -----------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    return await moderation.begin_chat_command(message, role, right=RIGHT) is not None


@router.message(cmd("blacklists"))
async def cmd_blacklists(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    found = await blacklist.triggers(message.chat.id)
    cfg = await blacklist.settings(message.chat.id)
    title = tr("blacklists.blacklisted_triggers_in", v=esc(message.chat.title or tr("common.this_chat")))
    if not found:
        await reply(message, tr("blacklists.there_are_none", title=title))
        return
    lines = [title, tr("blacklists.default_action", mode_text=esc(_mode_text(cfg['mode'], cfg['mode_seconds']))), ""]
    lines += [f"• {_trigger_line(t)}" for t in found[:MAX_SHOWN]]
    if len(found) > MAX_SHOWN:
        lines.append(f"…and {len(found) - MAX_SHOWN} more.")
    await reply(message, "\n".join(lines))


@router.message(cmd("addblacklist", "blacklist"))
async def cmd_addblacklist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    args = (command.args or "").strip()
    replied = replied_message(message)

    entries: list[tuple[str, str | None, str | None]] = []  # (trigger text, action spec, reason)
    problems: list[str] = []
    if replied is not None:
        # The replied message is the trigger; what follows the command is a reason and/or {action}.
        reason, spec = split_action(args)
        try:
            if getattr(replied, "sticker", None):
                trigger_text = _sticker_trigger(replied.sticker)
            else:
                trigger_text = blacklist.escape_wildcards(" ".join((replied.text or replied.caption or "").split()))
                if not trigger_text:
                    raise TriggerError(tr("blacklists.that_message_has_no_text_to"))
        except TriggerError as exc:
            await reply(message, esc(exc))
            return
        entries.append((trigger_text, spec, reason or None))
    elif args:
        for line in args.splitlines():
            if line.strip():
                text, spec = split_action(line)
                entries.append((_unquote(text), spec, None))
    else:
        await reply(
            message,
            tr("blacklists.give_me_triggers_one_per_line"),
        )
        return

    added: list[str] = []
    updated: list[str] = []
    for text, spec, reason in entries:
        try:
            pattern = blacklist.check(text)
            action, seconds = blacklist.parse_action(spec) if spec else (None, 0)
            if reason and len(reason) > 200:
                reason = truncate(reason, 200)
            outcome = await blacklist.add(message.chat.id, pattern, action, seconds, reason, message.from_user.id)
        except TriggerError as exc:
            problems.append(f"<code>{esc(truncate(text, 40))}</code>: {esc(exc)}")
            continue
        if outcome is None:
            full_text = tr("common.the_blacklist_is_full_triggers", MAX_TRIGGERS=blacklist.MAX_TRIGGERS)
            if full_text not in problems:
                problems.append(full_text)
            continue
        shown = f"<code>{esc(pattern)}</code>" + (f" {{{esc(_mode_text(action, seconds))}}}" if action else "")
        (added if outcome else updated).append(shown)

    lines: list[str] = []
    for label, items in ((tr("common.added"), added), (tr("common.updated"), updated)):
        if items:
            lines.append(f"<b>{label} {len(items)} trigger{'' if len(items) == 1 else 's'}:</b>")
            lines += [f"• {item}" for item in items[:MAX_SHOWN_ADDED]]
            if len(items) > MAX_SHOWN_ADDED:
                lines.append(f"…and {len(items) - MAX_SHOWN_ADDED} more.")
    if problems:
        lines.append("<b>Skipped:</b>")
        lines += [f"• {p}" for p in problems[:MAX_SHOWN_ADDED]]
    await reply(message, "\n".join(lines))


@router.message(cmd("unblacklist", "rmblacklist"))
async def cmd_unblacklist(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    args = (command.args or "").strip()
    replied = replied_message(message)
    texts: list[str] = []
    if args:
        texts = [_unquote(split_action(line)[0]) for line in args.splitlines() if line.strip()]
    elif replied is not None:
        try:
            if getattr(replied, "sticker", None):
                texts = [_sticker_trigger(replied.sticker)]
            else:
                texts = [blacklist.escape_wildcards(" ".join((replied.text or replied.caption or "").split()))]
        except TriggerError as exc:
            await reply(message, esc(exc))
            return
    if not texts:
        await reply(message, tr("blacklists.give_me_the_triggers_to_remove"))
        return

    removed: list[str] = []
    missing: list[str] = []
    for text in texts:
        try:
            pattern = blacklist.check(text)
        except TriggerError:
            missing.append(f"<code>{esc(truncate(text, 40))}</code>")
            continue
        (removed if await blacklist.remove(message.chat.id, pattern) else missing).append(f"<code>{esc(pattern)}</code>")

    lines = []
    if removed:
        lines.append(f"<b>Removed {len(removed)} trigger{'' if len(removed) == 1 else 's'}:</b>")
        lines += [f"• {item}" for item in removed[:MAX_SHOWN_ADDED]]
    if missing:
        lines.append("<b>Not in the blacklist:</b>")
        lines += [f"• {item}" for item in missing[:MAX_SHOWN_ADDED]]
    await reply(message, "\n".join(lines))


@router.message(cmd("blacklistmode", "blocklistmode"))
async def cmd_blacklistmode(message: Message, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    cfg = await blacklist.settings(message.chat.id)
    await reply(
        message,
        tr("blacklists.default_action_delete_blacklisted_messages_c", mode_text=esc(_mode_text(cfg['mode'], cfg['mode_seconds'])), opt=tr("common.on") if cfg['delete_matches'] else tr("common.off")),
    )


@router.message(cmd("setblacklistmode", "setblocklistmode"))
async def cmd_setblacklistmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    spec = (command.args or "").strip()
    usage = (
        tr("blacklists.use_setblacklistmode_lt_off_del_warn")
    )
    if not spec:
        await reply(message, usage)
        return
    try:
        mode, seconds = blacklist.parse_action(spec, allow_off=True)
    except TriggerError as exc:
        await reply(message, f"{esc(exc)}\n{usage}")
        return
    await blacklist.update_settings(message.chat.id, mode=mode, mode_seconds=seconds)
    if mode == "off":
        await reply(message, tr("blacklists.the_default_action_is_now_off"))
    else:
        await reply(message, tr("blacklists.the_default_action_is_now", mode_text=esc(_mode_text(mode, seconds))))


@router.message(cmd("blocklistdelete", "blacklistdelete"))
async def cmd_blocklistdelete(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    argument = (command.args or "").strip().lower()

    def text(on: bool) -> str:
        return (
            tr("blacklists.blacklisted_messages_will_be_deleted")
            if on
            else tr("blacklists.blacklisted_messages_will_be_kept_unless")
        )

    if not argument:
        await reply(message, tr("blacklists.use_blocklistdelete_on_or_off", text=text((await blacklist.settings(message.chat.id))['delete_matches'])))
        return
    if argument not in _ON_OFF:
        await reply(message, tr("blacklists.use_blocklistdelete_on_or_blocklistdelete_of"))
        return
    await blacklist.update_settings(message.chat.id, delete_matches=_ON_OFF[argument])
    await reply(message, text(_ON_OFF[argument]))


@router.message(cmd("unblacklistall"))
async def cmd_unblacklistall(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len(await blacklist.triggers(message.chat.id))
    if not count:
        await reply(message, tr("blacklists.the_blacklist_of_this_chat_is"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text=tr("common.remove_all"), callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, tr("blacklists.remove_all_triggers_of_this_chat", count=count), reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_unblacklistall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer(tr("common.only_the_owner_of_this_chat"), show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await blacklist.clear(message.chat.id)
        await callback.answer()
        await edit(message, tr("blacklists.done_triggers_removed", removed=removed), reply_markup=None)
    else:
        await callback.answer()
        await edit(message, tr("blacklists.cancelled_the_blacklist_is_unchanged"), reply_markup=None)


# --- Enforcement ----------------------------------------------------------------------------

async def _blacklisted(message: Message, bot: Bot) -> dict[str, Any] | bool:
    """A message that matches a trigger of its chat and whose sender is not exempt."""
    user = message.from_user
    if (
        user is None
        or user.is_bot
        or message.sender_chat is not None
        or message.chat.type not in GROUP_TYPES
        or getattr(message, "is_automatic_forward", None)
    ):
        return False
    triggers = await blacklist.triggers(message.chat.id)
    if not triggers:
        return False
    cfg = await blacklist.settings(message.chat.id)
    hit = None
    for trigger in blacklist.iter_matches(triggers, message.text or message.caption, getattr(message, "sticker", None)):
        action = trigger.action or cfg["mode"]
        if action != "off":
            seconds = trigger.seconds if trigger.action else cfg["mode_seconds"]
            hit = SimpleNamespace(trigger=trigger, action=action, seconds=seconds)
            break
    if hit is None:
        return False
    # Only now, with a match in hand, the (slower) checks of who is exempt.
    if (
        immunity.is_immune(user.id)
        or await roles.get_role(user.id) is not None  # anybody with a role in the bot
        or await approvals.is_approved(message.chat.id, user.id)
        or await moderation.is_chat_admin(bot, message.chat.id, user.id)
    ):
        return False
    hit.delete = hit.action == "del" or cfg["delete_matches"]
    return {"bl_hit": hit}


async def _warn(bot: Bot, message: Message, reason: str) -> str:
    """Warn the sender; at the chat's warn limit, ban them and start the count over."""
    from modules import warns

    user = message.from_user
    chat_id = message.chat.id
    target = SimpleNamespace(user_id=user.id, name=user.full_name)
    await warns.add_warn(chat_id, target, reason, moderation.Actor(bot.id, tr("blacklists.blacklist")))
    count = len(await warns.list_warns(chat_id, user.id))
    limit = await warns.get_limit(chat_id)
    if count < limit:
        return tr("common.warned", count=count, limit=limit)
    done = await warns.limit_action(bot, chat_id, user.id)
    await warns.clear_warns(chat_id, user.id)
    return tr("common.after_reaching_the_warn_limit", done=done, count=count, limit=limit)


@router.message(_blacklisted)
@router.edited_message(_blacklisted)
async def on_blacklisted(message: Message, bot: Bot, bl_hit: SimpleNamespace) -> None:
    chat_id = message.chat.id
    user = message.from_user
    if bl_hit.delete:
        with suppress(TelegramAPIError):
            await message.delete()
    if bl_hit.action == "del":
        return

    reason = bl_hit.trigger.reason or tr("blacklists.blacklisted_trigger", pattern=bl_hit.trigger.pattern)
    try:
        if bl_hit.action == "warn":
            done = await _warn(bot, message, reason)
        else:
            done = await enforce.punish(bot, chat_id, user.id, bl_hit.action, bl_hit.seconds)
    except ModerationError as exc:
        now = time.monotonic()
        if now - _complained.get(chat_id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat_id] = now
            await reply(message, tr("blacklists.used_a_blacklisted_trigger_but_i", id=user_link(user.id, user.full_name), exc=esc(exc)), quote=False)
        return
    await reply(
        message,
        tr("blacklists.has_been", id=user_link(user.id, user.full_name), done=done, reason_line=moderation.reason_line(bl_hit.trigger.reason or ''))
        + ("" if bl_hit.trigger.reason else tr("blacklists.reason_used_a_blacklisted_trigger")),
        quote=False,
    )
