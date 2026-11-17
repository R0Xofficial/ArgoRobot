"""Flood control"""

import re
import time
from contextlib import suppress
from typing import Any

from aiogram import Bot, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import approvals, enforce, flood, immunity, moderation, roles
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, MAX_DURATION, MIN_DURATION, ModerationError, parse_duration, perform
from core.roles import Role
from core.text import duration_text, esc, user_link, SWITCH as _ON_OFF, parse_switch
from core.i18n import tr, word

# Flood control is off until a chat admin sets a limit with /setflood. Bots are
# ignored, and so are chat admins and approved users.
ABOUT = "antiflood.about"

HELP = [
    ("/flood", "antiflood.help.show_the_current_flood_control"),
    ("/setflood <count|off>", "antiflood.help.act_on_anyone_who_sends", "common.admins"),
    ("/setfloodtimer <count> <duration>", "antiflood.help.act_on_anyone_who_sends_2", "common.admins"),
    ("/setfloodmode &lt;ban|kick|mute|tban|tmute&gt; [duration]", "antiflood.help.choose_what_happens_to_a", "common.admins"),
    ("/clearflood <on|off>", "antiflood.help.delete_the_whole_flood_without", "common.admins"),
    ("/antichannel <on|off>", "antiflood.help.ban_anyone_who_writes_as", "common.admins"),
]

HELP_NOTES = [
    (
        "common.examples",
        "antiflood.note.setflood_10_more_than_ten",
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
        return tr("antiflood.for", mode=word(mode), span_text=_span_text(settings['mode_seconds']))
    return word(mode)


def _state(enabled: bool) -> str:
    return tr("common.on") if enabled else tr("common.off")


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
    lines = [tr("antiflood.flood_control_in", chat_title=esc(chat_title)), ""]
    if not flood.active(settings):
        lines.append("Flood control is <b>off</b>.")
    else:
        lines.append(
            tr("antiflood.limit_more_than_messages_in_a", limit=limit) if limit else tr("antiflood.limit_off")
        )
        lines.append(
            tr("antiflood.timer_messages_within", timer=timer, span_text=_span_text(settings['timer_seconds']))
            if timer
            else tr("antiflood.timer_off")
        )
        lines.append(f"Action: <b>{esc(_mode_text(settings))}</b>")
        lines.append(f"Delete all flood messages: <b>{_state(settings['clear_flood'])}</b>")
    lines.append(f"Anti-channel: <b>{_state(settings['antichannel'])}</b>")
    return "\n".join(lines)


# --- Commands -------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """Settings are for chat admins and, unless the chat's owner turned it off (/staffhelp), the bot's staff."""
    return await moderation.begin_chat_command(message, role, right=RIGHT) is not None


@router.message(cmd("flood"))
async def cmd_flood(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    await reply(message, _settings_text(message.chat.title or tr("common.this_chat"), await flood.get(message.chat.id)))


@router.message(cmd("setflood"))
async def cmd_setflood(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    argument = (command.args or "").strip().lower()
    usage = tr("antiflood.use_setflood_lt_count_gt_to", MIN_COUNT=flood.MIN_COUNT, MAX_COUNT=flood.MAX_COUNT)
    if parse_switch(argument) is False or argument == "0":
        await flood.update(message.chat.id, flood_limit=0)
        await reply(message, tr("antiflood.the_limit_on_messages_in_a"))
        return
    count = _parse_count(argument)
    if count is None:
        await reply(message, usage)
        return
    await flood.update(message.chat.id, flood_limit=count)
    await reply(message, tr("antiflood.flood_control_will_act_on_anyone", count=count))


@router.message(cmd("setfloodtimer"))
async def cmd_setfloodtimer(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    parts = (command.args or "").lower().split()
    if len(parts) == 1 and parse_switch(parts[0]) is False:
        await flood.update(message.chat.id, timer_count=0, timer_seconds=0)
        await reply(message, tr("antiflood.the_timer_is_now_off"))
        return
    count = _parse_count(parts[0]) if len(parts) == 2 else None
    seconds = _parse_span(parts[1]) if len(parts) == 2 else None
    if count is None or seconds is None:
        await reply(
            message,
            tr("antiflood.use_setfloodtimer_lt_count_gt_lt", MIN_COUNT=flood.MIN_COUNT, MAX_COUNT=flood.MAX_COUNT),
        )
        return
    await flood.update(message.chat.id, timer_count=count, timer_seconds=seconds)
    await reply(
        message, tr("antiflood.flood_control_will_act_on_anyone_2", count=count, span_text=_span_text(seconds))
    )


@router.message(cmd("setfloodmode"))
async def cmd_setfloodmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    parts = (command.args or "").lower().split()
    usage = (
        tr("antiflood.use_setfloodmode_lt_ban_kick_mute")
    )
    if not parts or parts[0] not in flood.MODES or len(parts) > 2:
        await reply(message, usage)
        return
    mode = parts[0]
    seconds = 0
    if mode in flood.TIMED_MODES:
        duration = parse_duration(parts[1]) if len(parts) == 2 else None
        if duration is None or not MIN_DURATION <= duration <= MAX_DURATION:
            await reply(message, tr("antiflood.needs_a_duration_like_30m_2h", mode=mode))
            return
        seconds = int(duration.total_seconds())
    elif len(parts) == 2:
        await reply(message, tr("antiflood.doesn_t_take_a_duration_use", mode=mode))
        return
    settings = await flood.update(message.chat.id, mode=mode, mode_seconds=seconds)
    await reply(message, tr("antiflood.flood_control_will_now_anyone_who", mode_text=esc(_mode_text(settings))))


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
        message, command, role, "clear_flood", tr("antiflood.use_clearflood_on_or_clearflood_off"),
        lambda on: tr("antiflood.the_messages_that_trigger_flood_control")
        if on
        else tr("antiflood.the_messages_that_trigger_flood_control_2"),
    )


@router.message(cmd("antichannel"))
async def cmd_antichannel(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _toggle(
        message, command, role, "antichannel", tr("antiflood.use_antichannel_on_or_antichannel_off"),
        lambda on: tr("antiflood.anti_channel_is_on_anyone_speaking")
        if on
        else tr("antiflood.anti_channel_is_off"),
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
        or await roles.get_role(user.id) is not None  # anybody with a role in the bot
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
            await reply(message, tr("antiflood.is_flooding_the_chat_but_i", id=user_link(user.id, user.full_name), failure=esc(failure)))
        return
    await reply(message, tr("antiflood.flood_control_has_been", id=user_link(user.id, user.full_name), done=done), quote=False)


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
            await reply(message, tr("antiflood.anti_channel_is_on_but_i", exc=esc(exc)))
        return
    title = esc(channel.title or tr("antiflood.a_channel"))
    name = f'<a href="https://t.me/{esc(channel.username)}">{title}</a>' if getattr(channel, "username", None) else f"<b>{title}</b>"
    await reply(message, tr("antiflood.has_been_banned_speaking_as_a", name=name), quote=False)
