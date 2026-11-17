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
from core.i18n import tr

log = logging.getLogger(__name__)

ABOUT = "antiraid.about"

HELP_WHO = "common.admins"

HELP = [
    ("/antiraid [on|off]", "antiraid.help.turn_antiraid_on_or_off"),
    ("/raidlimit <joins> <time>", "antiraid.help.how_many_joins_within_how"),
    ("/raidtime <time>", "antiraid.help.how_long_a_raid_lasts"),
    ("/raidmode <mute|kick|ban>", "antiraid.help.what_happens_to_newcomers_during"),
]

router = Router(name="antiraid")

# Before the modules that greet and test newcomers, so that they can tell who the raid took away.
PRIORITY = -90

FAILURE_COOLDOWN = 300.0
_complained: dict[int, float] = {}


def _mode_text(mode: str) -> str:
    return {"mute": tr("antiraid.muted_until_the_raid_is_over"), "kick": "kicked", "ban": "banned"}[mode]


def _summary(settings: antiraid.Settings) -> str:
    raid = ""
    if antiraid.raging(settings):
        raid = tr("antiraid.raid_running_ends_in", duration_text=duration_text(int(settings.until - time.time())))
    return (
        tr("antiraid.antiraid_enabled_raid_joins_within_lasts", opt=tr("common.on") if settings.enabled else tr("common.off"), joins=settings.joins, duration_text=duration_text(settings.window), duration_text2=duration_text(settings.duration), mode_text=_mode_text(settings.mode))
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
            await _say(bot, chat, tr("antiraid.antiraid_is_on_but_i_can", exc=esc(exc)))
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
        tr("antiraid.raid_detected_accounts_joined_within_for", people=len(result['people']), duration_text=duration_text(settings.window), duration_text2=duration_text(settings.duration), mode_text=_mode_text(settings.mode)),
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
        await reply(message, tr("common.this_command_only_works_in_groups"))
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
            await reply(message, tr("antiraid.use_antiraid_on_or_antiraid_off"))
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
        tr("antiraid.use_raidlimit_lt_joins_gt_lt", MIN_JOINS=antiraid.MIN_JOINS, MAX_JOINS=antiraid.MAX_JOINS)
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
    await reply(message, tr("antiraid.a_raid_now_starts_when_accounts", joins=joins, duration_text=duration_text(window)))


@router.message(cmd("raidtime"))
async def cmd_raidtime(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    duration = _seconds((command.args or "").strip())
    if duration is None or not antiraid.MIN_DURATION <= duration <= antiraid.MAX_DURATION:
        await reply(message, tr("antiraid.use_raidtime_lt_time_gt_from"))
        return
    await antiraid.update(message.chat.id, duration=duration)
    await reply(message, tr("antiraid.a_raid_now_lasts", duration_text=duration_text(duration)))


@router.message(cmd("raidmode"))
async def cmd_raidmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    mode = (command.args or "").strip().lower()
    if mode not in antiraid.MODES:
        await reply(message, tr("antiraid.use_raidmode_lt_mute_kick_ban"))
        return
    await antiraid.update(message.chat.id, mode=mode)
    await reply(message, tr("antiraid.during_a_raid_newcomers_are_now", mode_text=_mode_text(mode)))


def _seconds(text: str, allow_seconds: bool = False) -> int | None:
    """30s, 2m, 1h, 1d -> seconds; None when it is not a time."""
    text = text.strip().lower()
    if allow_seconds and text.endswith("s") and text[:-1].isdigit():
        return int(text[:-1])
    duration = parse_duration(text)
    return int(duration.total_seconds()) if duration is not None else None
