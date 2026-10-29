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

from core import approvals, blacklist, enforce, moderation, whitelist
from core.blacklist import TriggerError
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import GROUP_TYPES, MAX_DURATION, MIN_DURATION, ModerationError, parse_duration, perform
from core.roles import Role
from core.targets import replied_message
from core.text import duration_text, esc, truncate, user_link

# Messages that match a trigger are deleted, or get a custom action. Blacklists do not
# affect group admins, approved users or whitelisted users.
ABOUT = """
Stop certain words or phrases from being said in the group. Matching messages are deleted, or get a custom action. Blacklists do not affect chat admins, approved users or whitelisted users, and edited messages are checked too.

Triggers are matched as whole words and ignore case, so "spam" catches "SPAM!" but not "spammer". Add several triggers at once with one per line.

The commands that change the blacklist are for chat admins with the right to change chat info.
"""

HELP = [
    ("/blacklists", "List the blacklisted triggers of this chat and the default action."),
    ("/addblacklist <reply/triggers>", "Add one or more triggers, one per line, or reply to a message to blacklist its text. /blacklist works too.", "admins"),
    ("/unblacklist <triggers>", "Remove triggers, one per line, or reply to a message or sticker.", "admins"),
    ("/blacklistmode", "Show the default action and whether blacklisted messages are deleted.", "admins"),
    ("/setblacklistmode <off|del|warn|ban|kick|mute|tban|tmute>", "Set the default action. tban and tmute need a duration. off turns the default off, leaving only triggers with their own action.", "admins"),
    ("/blocklistdelete <on|off>", "Delete blacklisted messages, whatever the action. Default: on. With off, only the action delete removes a message.", "admins"),
    ("/unblacklistall", "Remove every trigger at once. Asks for confirmation.", "chat owner"),
]

HELP_NOTES = [
    (
        "Custom action per trigger",
        """
        Append {action} after a trigger to override the default action. Example: /addblacklist "ban me" {ban} bans anyone who says ban me.
        Actions: delete, ban, mute, kick, warn, tban <time>, tmute <time>.
        """,
    ),
    (
        "Match modifiers",
        """
        exact:word - match only messages equal to word.
        prefix:word - match only messages that start with word.
        sticker:emoji - match a sticker by its emoji.
        stickerpack:name - match a whole sticker pack (the part after t.me/addstickers/).
        """,
    ),
    (
        "Wildcards",
        """
        ? - any single character except spaces.
        * - any run of characters except spaces (great for URLs).
        ** - any run of characters, spaces included.
        Put a backslash before * or ? to match them literally.
        """,
    ),
    (
        "Stickers",
        """
        Reply to a sticker with /blacklist to blacklist its pack. Add a custom reason and/or action: /blacklist ugly pack {ban}.
        """,
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

_ON_OFF = {"on": True, "yes": True, "off": False, "no": False}
_ACTION_SUFFIX = re.compile(r"\{([^{}]*)\}\s*$")
_QUOTES = "\"'“”‘’"
_complained: dict[int, float] = {}


# --- Wording and parsing -----------------------------------------------------------------

def _action_name(action: str | None) -> str:
    return "delete" if action == "del" else str(action)


def _mode_text(mode: str, seconds: int) -> str:
    if mode == "off":
        return "off"
    if mode in enforce.TIMED_MODES:
        return f"{mode} for {duration_text(seconds)}"
    return _action_name(mode)


def _trigger_line(trigger: blacklist.Trigger) -> str:
    line = f"<code>{esc(trigger.pattern)}</code>"
    if trigger.action:
        line += f" {{{esc(_mode_text(trigger.action, trigger.seconds))}}}"
    return line


def parse_action(spec: str, *, allow_off: bool = False) -> tuple[str, int]:
    """'ban', 'delete', 'tmute 30m' -> ('ban', 0), ('del', 0), ('tmute', 1800)."""
    words = spec.lower().split()
    if not words:
        raise TriggerError("The action is empty.")
    name = blacklist.ALIASES.get(words[0], words[0])
    if name == "off" and not allow_off:
        name = ""
    if name not in blacklist.ACTIONS and not (allow_off and name == "off"):
        options = "delete, ban, mute, kick, warn, tban <time>, tmute <time>"
        raise TriggerError(f"Unknown action \"{words[0]}\". Use one of: {options}.")
    if name in blacklist.TIMED:
        duration = parse_duration(words[1]) if len(words) == 2 else None
        if duration is None or not MIN_DURATION <= duration <= MAX_DURATION:
            raise TriggerError(f"{name} needs a duration, like 30m, 2h, 3d or 1w (from 1 minute to 365 days).")
        return name, int(duration.total_seconds())
    if len(words) > 1:
        raise TriggerError(f"{name} doesn't take a duration. Use tban or tmute for a temporary action.")
    return name, 0


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
    raise TriggerError("That sticker has neither a pack nor an emoji to match.")


# --- Commands -----------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=RIGHT) is not None


@router.message(cmd("blacklists"))
async def cmd_blacklists(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    found = await blacklist.triggers(message.chat.id)
    cfg = await blacklist.settings(message.chat.id)
    title = f"<b>Blacklisted triggers in {esc(message.chat.title or 'this chat')}</b>"
    if not found:
        await reply(message, f"{title}\n\nThere are none.")
        return
    lines = [title, f"Default action: <b>{esc(_mode_text(cfg['mode'], cfg['mode_seconds']))}</b>", ""]
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
                    raise TriggerError("That message has no text to blacklist.")
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
            "Give me triggers, one per line, or reply to a message or sticker.\n"
            "Example: /addblacklist spam word\nAppend {action} to a trigger to override the default action.",
        )
        return

    added: list[str] = []
    updated: list[str] = []
    for text, spec, reason in entries:
        try:
            pattern = blacklist.check(text)
            action, seconds = parse_action(spec) if spec else (None, 0)
            if reason and len(reason) > 200:
                reason = truncate(reason, 200)
            outcome = await blacklist.add(message.chat.id, pattern, action, seconds, reason, message.from_user.id)
        except TriggerError as exc:
            problems.append(f"<code>{esc(truncate(text, 40))}</code>: {esc(exc)}")
            continue
        if outcome is None:
            full_text = f"The blacklist is full ({blacklist.MAX_TRIGGERS} triggers)."
            if full_text not in problems:
                problems.append(full_text)
            continue
        shown = f"<code>{esc(pattern)}</code>" + (f" {{{esc(_mode_text(action, seconds))}}}" if action else "")
        (added if outcome else updated).append(shown)

    lines: list[str] = []
    for label, items in (("Added", added), ("Updated", updated)):
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
        await reply(message, "Give me the triggers to remove, one per line, or reply to a message or sticker.")
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
        f"Default action: <b>{esc(_mode_text(cfg['mode'], cfg['mode_seconds']))}</b>\n"
        f"Delete blacklisted messages: <b>{'on' if cfg['delete_matches'] else 'off'}</b>\n"
        "Change it with /setblacklistmode.",
    )


@router.message(cmd("setblacklistmode", "setblocklistmode"))
async def cmd_setblacklistmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    spec = (command.args or "").strip()
    usage = (
        "Use /setblacklistmode <off|del|warn|ban|kick|mute|tban|tmute>.\n"
        "tban and tmute need a duration, like 30m, 2h, 3d or 1w."
    )
    if not spec:
        await reply(message, usage)
        return
    try:
        mode, seconds = parse_action(spec, allow_off=True)
    except TriggerError as exc:
        await reply(message, f"{esc(exc)}\n{usage}")
        return
    await blacklist.update_settings(message.chat.id, mode=mode, mode_seconds=seconds)
    if mode == "off":
        await reply(message, "The default action is now <b>off</b>: only triggers with their own {action} do anything.")
    else:
        await reply(message, f"The default action is now <b>{esc(_mode_text(mode, seconds))}</b>.")


@router.message(cmd("blocklistdelete", "blacklistdelete"))
async def cmd_blocklistdelete(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    argument = (command.args or "").strip().lower()

    def text(on: bool) -> str:
        return (
            "Blacklisted messages will be <b>deleted</b>."
            if on
            else "Blacklisted messages will be <b>kept</b> (unless the action is delete)."
        )

    if not argument:
        await reply(message, f"{text((await blacklist.settings(message.chat.id))['delete_matches'])}\nUse /blocklistdelete on or off.")
        return
    if argument not in _ON_OFF:
        await reply(message, "Use /blocklistdelete on or /blocklistdelete off.")
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
        await reply(message, "The blacklist of this chat is empty.")
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Remove all", callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text="Cancel", callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, f"Remove all <b>{count}</b> triggers of this chat? This cannot be undone.", reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_unblacklistall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer("Only the owner of this chat can do this.", show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await blacklist.clear(message.chat.id)
        await callback.answer()
        await edit(message, f"Done. <b>{removed}</b> triggers removed.", reply_markup=None)
    else:
        await callback.answer()
        await edit(message, "Cancelled. The blacklist is unchanged.", reply_markup=None)


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
        await whitelist.is_protected(user.id)
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
    await warns.add_warn(chat_id, target, reason, moderation.Actor(bot.id, "Blacklist"))
    count = len(await warns.list_warns(chat_id, user.id))
    limit = await warns.get_limit(chat_id)
    if count < limit:
        return f"warned ({count}/{limit})"
    await perform(bot.ban_chat_member(chat_id, user.id))
    await warns.clear_warns(chat_id, user.id)
    return f"banned after reaching the warn limit ({count}/{limit})"


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

    reason = bl_hit.trigger.reason or f"Blacklisted trigger: {bl_hit.trigger.pattern}"
    try:
        if bl_hit.action == "warn":
            done = await _warn(bot, message, reason)
        else:
            done = await enforce.punish(bot, chat_id, user.id, bl_hit.action, bl_hit.seconds)
    except ModerationError as exc:
        now = time.monotonic()
        if now - _complained.get(chat_id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat_id] = now
            await reply(message, f"{user_link(user.id, user.full_name)} used a blacklisted trigger, but I can't act: {esc(exc)}", quote=False)
        return
    await reply(
        message,
        f"{user_link(user.id, user.full_name)} has been {done}.{moderation.reason_line(bl_hit.trigger.reason or '')}"
        + ("" if bl_hit.trigger.reason else "\nReason: used a blacklisted trigger."),
        quote=False,
    )
