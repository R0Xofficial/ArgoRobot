"""Flood control"""

import re
import time
from contextlib import suppress
from typing import Any

from aiogram import Bot, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import approvals, enforce, flood, immunity, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, MAX_DURATION, MIN_DURATION, ModerationError, parse_duration, perform
from core.roles import Role
from core.text import duration_text, esc, user_link, SWITCH as _ON_OFF, parse_switch

# By default the bot mutes anyone who sends more than 10 messages in a row. Bots are
# ignored, and so are chat admins and approved users.
ABOUT = """
Takes action on people who flood the chat with messages. By default I mute anyone who sends more than 10 messages in a row. Bots, chat admins and approved users are ignored, and an album of photos counts as one message.

Two rules can run side by side: the limit counts messages in a row from one person, the timer counts messages from one person within a time window, whoever else writes in between.

The /set... commands are for chat admins with the right to change chat info.
"""

HELP = [
    ("/flood", "Show the current flood-control settings."),
    ("/setflood <count|off>", "Act on anyone who sends more than count messages in a row (2 to 100). off disables this rule.", "admins"),
    ("/setfloodtimer <count> <duration>", "Act on anyone who sends count messages within duration, like 10s, 2m or 1h. off disables the timer.", "admins"),
    ("/setfloodmode &lt;ban|kick|mute|tban|tmute&gt; [duration]", "Choose what happens to a flooder. tban and tmute are temporary and need a duration like 30m, 2h or 3d. Default: mute.", "admins"),
    ("/clearflood <on|off>", "Delete the whole flood. Without it only the messages over the limit are deleted.", "admins"),
    ("/antichannel <on|off>", "Ban anyone who writes as a channel and make them talk as themselves. Anonymous admins and the chat's linked channel are not affected.", "admins"),
]

HELP_NOTES = [
    (
        "Examples",
        """
        /setflood 10 - more than ten messages in a row.
        /setfloodtimer 5 10s - five messages within ten seconds.
        /setfloodmode tmute 30m - mute flooders for thirty minutes.
        """,
    ),
]

router = Router(name="antiflood")

# Count messages before the other modules see them, but after global bans (-100) have
# removed banned users: their deleted messages should not count.
PRIORITY = -60

RIGHT = "can_change_info"  # the admin right needed to change these settings
FAILURE_COOLDOWN = 300.0  # seconds between "I can't act" complaints in the same chat

_SPAN = re.compile(r"^(\d{1,6})([smh]?)$", re.IGNORECASE)
_SPAN_UNITS = {"": 1, "s": 1, "m": 60, "h": 3600}
_complained: dict[int, float] = {}


# --- Wording --------------------------------------------------------------------

_span_text = duration_text


def _mode_text(settings: dict[str, Any]) -> str:
    mode = settings["mode"]
    if mode in flood.TIMED_MODES:
        return f"{mode} for {_span_text(settings['mode_seconds'])}"
    return mode


def _state(enabled: bool) -> str:
    return "on" if enabled else "off"


def _parse_span(text: str) -> int | None:
    """10s, 2m, 1h (a bare number is seconds) -> seconds, or None."""
    match = _SPAN.match(text.strip())
    if match is None:
        return None
    seconds = int(match.group(1)) * _SPAN_UNITS[match.group(2).lower()]
    return seconds if flood.MIN_SECONDS <= seconds <= flood.MAX_SECONDS else None


def _parse_count(text: str) -> int | None:
    if not text.strip().isdigit():
        return None
    count = int(text)
    return count if flood.MIN_COUNT <= count <= flood.MAX_COUNT else None


def _settings_text(chat_title: str, settings: dict[str, Any]) -> str:
    limit = settings["flood_limit"]
    timer = settings["timer_count"]
    lines = [f"<b>Flood control in {esc(chat_title)}</b>", ""]
    if not flood.active(settings):
        lines.append("Flood control is <b>off</b>.")
    else:
        lines.append(
            f"Limit: more than <b>{limit}</b> messages in a row" if limit else "Limit: <b>off</b>"
        )
        lines.append(
            f"Timer: <b>{timer}</b> messages within <b>{_span_text(settings['timer_seconds'])}</b>"
            if timer
            else "Timer: <b>off</b>"
        )
        lines.append(f"Action: <b>{esc(_mode_text(settings))}</b>")
        lines.append(f"Delete all flood messages: <b>{_state(settings['clear_flood'])}</b>")
    lines.append(f"Anti-channel: <b>{_state(settings['antichannel'])}</b>")
    return "\n".join(lines)


# --- Commands -------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """Settings are for chat admins; the bot's staff does not run other people's chats."""
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=RIGHT) is not None


@router.message(cmd("flood"))
async def cmd_flood(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    await reply(message, _settings_text(message.chat.title or "this chat", await flood.get(message.chat.id)))


@router.message(cmd("setflood"))
async def cmd_setflood(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    argument = (command.args or "").strip().lower()
    usage = f"Use /setflood &lt;count&gt; ({flood.MIN_COUNT} to {flood.MAX_COUNT}) or /setflood off."
    if parse_switch(argument) is False or argument == "0":
        await flood.update(message.chat.id, flood_limit=0)
        await reply(message, "The limit on messages in a row is now <b>off</b>.")
        return
    count = _parse_count(argument)
    if count is None:
        await reply(message, usage)
        return
    await flood.update(message.chat.id, flood_limit=count)
    await reply(message, f"Flood control will act on anyone who sends more than <b>{count}</b> messages in a row.")


@router.message(cmd("setfloodtimer"))
async def cmd_setfloodtimer(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    parts = (command.args or "").lower().split()
    if len(parts) == 1 and parse_switch(parts[0]) is False:
        await flood.update(message.chat.id, timer_count=0, timer_seconds=0)
        await reply(message, "The timer is now <b>off</b>.")
        return
    count = _parse_count(parts[0]) if len(parts) == 2 else None
    seconds = _parse_span(parts[1]) if len(parts) == 2 else None
    if count is None or seconds is None:
        await reply(
            message,
            f"Use /setfloodtimer &lt;count&gt; &lt;duration&gt;, for example /setfloodtimer 5 10s, or /setfloodtimer off.\n"
            f"The count is {flood.MIN_COUNT} to {flood.MAX_COUNT}; the duration is like 10s, 2m or 1h (up to 24 hours).",
        )
        return
    await flood.update(message.chat.id, timer_count=count, timer_seconds=seconds)
    await reply(
        message, f"Flood control will act on anyone who sends <b>{count}</b> messages within <b>{_span_text(seconds)}</b>."
    )


@router.message(cmd("setfloodmode"))
async def cmd_setfloodmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    parts = (command.args or "").lower().split()
    usage = (
        "Use /setfloodmode &lt;ban|kick|mute|tban|tmute&gt; [duration].\n"
        "tban and tmute need a duration, like 30m, 2h, 3d or 1w."
    )
    if not parts or parts[0] not in flood.MODES or len(parts) > 2:
        await reply(message, usage)
        return
    mode = parts[0]
    seconds = 0
    if mode in flood.TIMED_MODES:
        duration = parse_duration(parts[1]) if len(parts) == 2 else None
        if duration is None or not MIN_DURATION <= duration <= MAX_DURATION:
            await reply(message, f"{mode} needs a duration, like 30m, 2h, 3d or 1w (from 1 minute to 365 days).")
            return
        seconds = int(duration.total_seconds())
    elif len(parts) == 2:
        await reply(message, f"{mode} doesn't take a duration. Use tban or tmute for a temporary action.")
        return
    settings = await flood.update(message.chat.id, mode=mode, mode_seconds=seconds)
    await reply(message, f"Flood control will now <b>{esc(_mode_text(settings))}</b> anyone who floods the chat.")


async def _toggle(message: Message, command: CommandObject, role: Role | None, column: str, usage: str, text) -> None:
    if not await _admin_only(message, role):
        return
    argument = (command.args or "").strip().lower()
    if not argument:
        current = (await flood.get(message.chat.id))[column]
        await reply(message, f"{text(current)}\n{usage}")
        return
    if argument not in _ON_OFF:
        await reply(message, usage)
        return
    enabled = _ON_OFF[argument]
    await flood.update(message.chat.id, **{column: enabled})
    await reply(message, text(enabled))


@router.message(cmd("clearflood"))
async def cmd_clearflood(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _toggle(
        message, command, role, "clear_flood", "Use /clearflood on or /clearflood off.",
        lambda on: "The messages that trigger flood control will be <b>deleted</b>."
        if on
        else "The messages that trigger flood control will be <b>kept</b>.",
    )


@router.message(cmd("antichannel"))
async def cmd_antichannel(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _toggle(
        message, command, role, "antichannel", "Use /antichannel on or /antichannel off.",
        lambda on: "Anti-channel is <b>on</b>: anyone speaking as a channel is banned and must talk as themselves."
        if on
        else "Anti-channel is <b>off</b>.",
    )


# --- Enforcement ---------------------------------------------------------------------

def _exempt_service(message: Message) -> bool:
    return bool(
        getattr(message, "new_chat_members", None)
        or getattr(message, "left_chat_member", None)
        or getattr(message, "pinned_message", None)
        or getattr(message, "is_automatic_forward", None)
    )


async def _flooding(message: Message, bot: Bot) -> dict[str, Any] | bool:
    """A message that pushes its sender over the chat's flood limit."""
    user = message.from_user
    if (
        user is None
        or user.is_bot
        or message.sender_chat is not None
        or message.chat.type not in GROUP_TYPES
        or _exempt_service(message)
    ):
        return False
    settings = await flood.get(message.chat.id)
    if not flood.active(settings):
        return False
    ids = flood.record(
        settings, message.chat.id, user.id, message.message_id, time.monotonic(),
        getattr(message, "media_group_id", None),
    )
    if ids is None:
        return False
    # Only now, when someone is over the limit, the (slow) checks of who is exempt.
    if (
        immunity.is_immune(user.id)
        or await approvals.is_approved(message.chat.id, user.id)
        or await moderation.is_chat_admin(bot, message.chat.id, user.id)
    ):
        return False
    return {"flood_ids": ids, "flood_settings": settings}


async def _punish(bot: Bot, chat_id: int, user_id: int, settings: dict[str, Any]) -> str:
    return await enforce.punish(bot, chat_id, user_id, settings["mode"], settings["mode_seconds"])


@router.message(_flooding)
async def on_flood(message: Message, bot: Bot, flood_ids: list[int], flood_settings: dict[str, Any]) -> None:
    chat_id = message.chat.id
    user = message.from_user
    if flood_settings["clear_flood"]:
        doomed = list(flood_ids)  # the whole flood
    else:
        # Always the messages over the limit: the one that tripped it, and any beyond it.
        limit = flood_settings["flood_limit"]
        doomed = flood_ids[limit:] if limit and len(flood_ids) > limit else []
        if message.message_id not in doomed:
            doomed.append(message.message_id)
    try:
        done = await _punish(bot, chat_id, user.id, flood_settings)
    except ModerationError as exc:
        done = None
        failure = exc
    with suppress(TelegramAPIError):
        await bot.delete_messages(chat_id, doomed)
    if done is None:
        now = time.monotonic()
        if now - _complained.get(chat_id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat_id] = now
            await reply(message, f"{user_link(user.id, user.full_name)} is flooding the chat, but I can't act: {esc(failure)}")
        return
    await reply(message, f"Flood control: {user_link(user.id, user.full_name)} has been {done}.", quote=False)


async def _channel_speaking(message: Message) -> bool:
    """A message written as a channel in a chat that doesn't allow that."""
    channel = message.sender_chat
    if channel is None or message.chat.type not in GROUP_TYPES:
        return False
    if channel.id == message.chat.id or getattr(message, "is_automatic_forward", None):
        return False  # an anonymous admin, or the chat's own linked channel
    return (await flood.get(message.chat.id))["antichannel"]


@router.message(_channel_speaking)
async def on_channel(message: Message, bot: Bot) -> None:
    channel = message.sender_chat
    with suppress(TelegramAPIError):
        await message.delete()
    try:
        await perform(bot.ban_chat_sender_chat(message.chat.id, channel.id))
    except ModerationError as exc:
        now = time.monotonic()
        if now - _complained.get(message.chat.id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[message.chat.id] = now
            await reply(message, f"Anti-channel is on, but I can't ban channels: {esc(exc)}")
        return
    title = esc(channel.title or "A channel")
    name = f'<a href="https://t.me/{esc(channel.username)}">{title}</a>' if getattr(channel, "username", None) else f"<b>{title}</b>"
    await reply(message, f"{name} has been banned: speaking as a channel isn't allowed here. Talk as yourself.", quote=False)
