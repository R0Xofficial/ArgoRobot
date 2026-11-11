"""Antiraid: stop a flood of new accounts"""

import logging
import time
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware, Router
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import antiraid, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, ModerationError, parse_duration
from core.roles import Role
from core.text import duration_text, esc, parse_switch

log = logging.getLogger(__name__)

ABOUT = """
A raid is a crowd of new accounts joining a chat within moments, usually to flood it. With antiraid on, I count the joins: when too many come in a short time, the chat is in a raid for a while, and everybody who joins during it is dealt with, as are the people who made it a raid.

By default 10 joins within 1 minute make a raid, and a raid lasts 1 hour. Newcomers are muted until the raid is over (they can write once it ends), or kicked, or banned. Chat admins, approved users, bots and the bot's staff are never touched.

Antiraid is off until a chat admin turns it on, and I need the right to restrict users.
"""

HELP_WHO = "admins"

HELP = [
    ("/antiraid [on|off]", "Turn antiraid on or off, or show its settings. Turning it off also ends a running raid."),
    ("/raidlimit <joins> <time>", "How many joins within how much time make a raid. Example: /raidlimit 10 1m. Default: 10 within 1 minute."),
    ("/raidtime <time>", "How long a raid lasts, like 30m, 2h or 1d. Default: 1 hour."),
    ("/raidmode <mute|kick|ban>", "What happens to newcomers during a raid. Default: mute until the raid is over."),
]

router = Router(name="antiraid")

# Before the modules that greet and test newcomers, so that they can tell who the raid took away.
PRIORITY = -90

FAILURE_COOLDOWN = 300.0
_complained: dict[int, float] = {}


def _mode_text(mode: str) -> str:
    return {"mute": "muted until the raid is over", "kick": "kicked", "ban": "banned"}[mode]


def _summary(settings: antiraid.Settings) -> str:
    raid = ""
    if antiraid.raging(settings):
        raid = f"\n• <b>Raid:</b> <code>running</code>, ends in <code>{duration_text(int(settings.until - time.time()))}</code>"
    return (
        "<b>Antiraid</b>\n"
        f"• <b>Enabled:</b> <code>{'on' if settings.enabled else 'off'}</code>\n"
        f"• <b>Raid:</b> <code>{settings.joins}</code> joins within <code>{duration_text(settings.window)}</code>\n"
        f"• <b>Lasts:</b> <code>{duration_text(settings.duration)}</code>\n"
        f"• <b>Newcomers are:</b> <code>{_mode_text(settings.mode)}</code>"
        + raid
    )


# --- The raid itself -----------------------------------------------------------------------

async def react(bot, chat, users: list) -> None:
    """Newcomers came: count them, and say so when a raid starts. Never lets a failure reach the caller."""
    try:
        result = await antiraid.joined(bot, chat, users)
    except ModerationError as exc:
        now = time.monotonic()
        if now - _complained.get(chat.id, -FAILURE_COOLDOWN) >= FAILURE_COOLDOWN:
            _complained[chat.id] = now
            await _say(bot, chat, f"Antiraid is on, but I can't act: {esc(exc)}")
        return
    except Exception:
        log.exception("Antiraid failed in chat %s", chat.id)
        return
    if result is None or not result["started"]:
        return
    settings = result["settings"]
    await _say(
        bot,
        chat,
        f"<b>Raid detected.</b> {len(result['people'])} accounts joined within {duration_text(settings.window)}. "
        f"For the next {duration_text(settings.duration)}, newcomers are {_mode_text(settings.mode)}.",
    )


async def _say(bot, chat, text: str) -> None:
    try:
        await bot.send_message(chat.id, text)
    except Exception:
        log.warning("Antiraid could not write to chat %s", chat.id)


class RaidMiddleware(BaseMiddleware):
    """Looks at every "joined" message before the handlers do, without taking it away from them."""

    async def __call__(self, handler: Callable[[Any, dict[str, Any]], Awaitable[Any]], event: Any, data: dict[str, Any]) -> Any:
        try:
            if getattr(event.chat, "type", None) in GROUP_TYPES and event.new_chat_members:
                await react(data.get("bot") or event.bot, event.chat, list(event.new_chat_members))
        except Exception:
            log.exception("Handling a join for antiraid failed")
        return await handler(event, data)


def _present(member) -> bool:
    status = str(getattr(member.status, "value", member.status))
    return status in ("member", "administrator", "creator") or (status == "restricted" and getattr(member, "is_member", True))


class RaidUpdateMiddleware(BaseMiddleware):
    """The same for the member updates: in a chat with hidden members there is no "joined" message."""

    async def __call__(self, handler: Callable[[Any, dict[str, Any]], Awaitable[Any]], event: Any, data: dict[str, Any]) -> Any:
        try:
            if (
                getattr(event.chat, "type", None) in GROUP_TYPES
                and not _present(event.old_chat_member)
                and _present(event.new_chat_member)
                and str(getattr(event.new_chat_member.status, "value", event.new_chat_member.status)) != "creator"
            ):
                await react(data.get("bot") or event.bot, event.chat, [event.new_chat_member.user])
        except Exception:
            log.exception("Handling a member update for antiraid failed")
        return await handler(event, data)


router.message.outer_middleware(RaidMiddleware())
router.chat_member.outer_middleware(RaidUpdateMiddleware())


# --- Commands -------------------------------------------------------------------------------

async def _allowed(message: Message, role: Role | None) -> bool:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return False
    return await moderation.begin_chat_command(message, role) is not None


@router.message(cmd("antiraid"))
async def cmd_antiraid(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    chat_id = message.chat.id
    argument = (command.args or "").strip()
    if argument:
        wanted = parse_switch(argument)
        if wanted is None:
            await reply(message, "Use /antiraid on or /antiraid off.")
            return
        await antiraid.update(chat_id, enabled=wanted)
        if not wanted:
            await antiraid.end(message.bot, chat_id)
    await reply(message, _summary(await antiraid.get(chat_id)))


@router.message(cmd("raidlimit"))
async def cmd_raidlimit(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    words = (command.args or "").split()
    usage = (
        f"Use /raidlimit &lt;joins&gt; &lt;time&gt;, with joins from {antiraid.MIN_JOINS} to {antiraid.MAX_JOINS} "
        "and a time like 30s, 1m or 10m. Example: /raidlimit 10 1m."
    )
    if len(words) != 2 or not words[0].isdigit():
        await reply(message, usage)
        return
    joins = int(words[0])
    window = _seconds(words[1], allow_seconds=True)
    if not antiraid.MIN_JOINS <= joins <= antiraid.MAX_JOINS or window is None or not antiraid.MIN_WINDOW <= window <= antiraid.MAX_WINDOW:
        await reply(message, usage)
        return
    await antiraid.update(message.chat.id, joins=joins, window=window)
    await reply(message, f"A raid now starts when <b>{joins}</b> accounts join within <b>{duration_text(window)}</b>.")


@router.message(cmd("raidtime"))
async def cmd_raidtime(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    duration = _seconds((command.args or "").strip())
    if duration is None or not antiraid.MIN_DURATION <= duration <= antiraid.MAX_DURATION:
        await reply(message, "Use /raidtime &lt;time&gt;, from 1 minute to 7 days, like 30m, 2h or 1d.")
        return
    await antiraid.update(message.chat.id, duration=duration)
    await reply(message, f"A raid now lasts <b>{duration_text(duration)}</b>.")


@router.message(cmd("raidmode"))
async def cmd_raidmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    mode = (command.args or "").strip().lower()
    if mode not in antiraid.MODES:
        await reply(message, "Use /raidmode &lt;mute|kick|ban&gt;.")
        return
    await antiraid.update(message.chat.id, mode=mode)
    await reply(message, f"During a raid, newcomers are now <b>{_mode_text(mode)}</b>.")


def _seconds(text: str, allow_seconds: bool = False) -> int | None:
    """30s, 2m, 1h, 1d -> seconds; None when it is not a time."""
    text = text.strip().lower()
    if allow_seconds and text.endswith("s") and text[:-1].isdigit():
        return int(text[:-1])
    duration = parse_duration(text)
    return int(duration.total_seconds()) if duration is not None else None
