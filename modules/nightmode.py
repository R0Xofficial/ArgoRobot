"""Nightmode: close the chat for the night"""

from datetime import datetime, timezone

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import moderation, nightmode
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.text import esc, parse_switch

ABOUT = """
Nightmode closes the chat at the hour you choose and opens it again in the morning. While it is closed, nobody but the admins can write; the chat's permissions from before are saved and put back exactly as they were.

By default the night lasts from 23:00 to 07:00 in UTC. Set your own hours with /nighthours (a night may pass midnight) and your time zone with /nighttz, for example /nighttz Europe/Warsaw or /nighttz UTC+1.

I announce both the closing and the opening. Nightmode is off until a chat admin turns it on, and I need the right to restrict members.
"""

HELP_WHO = "admins"

HELP = [
    ("/nightmode [on|off]", "Turn nightmode on or off, or show its settings."),
    ("/nighthours <from> <to>", "When the night starts and ends, like /nighthours 23:00 07:00. Default: 23:00 to 07:00."),
    ("/nighttz <zone>", "The time zone of those hours: a name like Europe/Warsaw, or an offset like UTC+1. Default: UTC."),
]

router = Router(name="nightmode")


def _summary(settings: nightmode.Settings) -> str:
    night = settings.enabled and nightmode.is_night(settings)
    now = datetime.now(timezone.utc).astimezone(nightmode.zone_of(settings.zone))
    return (
        "<b>Nightmode</b>\n"
        f"• <b>Enabled:</b> <code>{'on' if settings.enabled else 'off'}</code>\n"
        f"• <b>Night:</b> <code>{nightmode.clock_text(settings.start)}</code> to <code>{nightmode.clock_text(settings.end)}</code>\n"
        f"• <b>Time zone:</b> <code>{esc(settings.zone)}</code> (now <code>{now:%H:%M}</code> there)\n"
        f"• <b>Chat right now:</b> <code>{'closed' if settings.active else 'open'}</code>"
        + ("" if not (night and not settings.active) else "\nThe night has begun; I close the chat within half a minute.")
    )


async def _allowed(message: Message, role: Role | None) -> bool:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return False
    return await moderation.begin_chat_command(message, role) is not None


async def _apply(message: Message) -> str | None:
    """Close or open the chat now when the clock says so; the sweep would do it within half a minute anyway."""
    try:
        what = await nightmode.reconcile(message.bot, message.chat.id)
    except ModerationError as exc:
        return str(exc)
    except Exception as exc:
        return f"I can't change the permissions of this chat: {getattr(exc, 'message', exc)}"
    if what:
        await nightmode.announce(message.bot, message.chat.id, what, await nightmode.get(message.chat.id))
    return None


@router.message(cmd("nightmode"))
async def cmd_nightmode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    chat_id = message.chat.id
    argument = (command.args or "").strip()
    problem = None
    if argument:
        wanted = parse_switch(argument)
        if wanted is None:
            await reply(message, "Use /nightmode on or /nightmode off.")
            return
        await nightmode.update(chat_id, enabled=wanted)
        problem = await _apply(message)
    text = _summary(await nightmode.get(chat_id))
    if problem:
        text += f"\n{esc(problem)}"
    await reply(message, text)


@router.message(cmd("nighthours"))
async def cmd_nighthours(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    words = (command.args or "").replace("-", " ").replace("to", " ").split()
    start = nightmode.parse_clock(words[0]) if len(words) == 2 else None
    end = nightmode.parse_clock(words[1]) if len(words) == 2 else None
    if start is None or end is None or start == end:
        await reply(
            message,
            "Use /nighthours &lt;from&gt; &lt;to&gt;, two different hours like <code>23:00 07:00</code> (24-hour clock). "
            "The night may pass midnight.",
        )
        return
    await nightmode.update(message.chat.id, start=start, end=end)
    problem = await _apply(message)
    text = (
        f"The night now lasts from <b>{nightmode.clock_text(start)}</b> to <b>{nightmode.clock_text(end)}</b> "
        f"({esc((await nightmode.get(message.chat.id)).zone)})."
    )
    if problem:
        text += f"\n{esc(problem)}"
    await reply(message, text)


@router.message(cmd("nighttz"))
async def cmd_nighttz(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _allowed(message, role):
        return
    parsed = nightmode.parse_zone(command.args or "")
    if parsed is None:
        await reply(
            message,
            "Use /nighttz &lt;zone&gt;: a name like <code>Europe/Warsaw</code> (it follows summer time), "
            "or an offset like <code>UTC+1</code> or <code>-5:30</code>.",
        )
        return
    name, _ = parsed
    await nightmode.update(message.chat.id, zone=name)
    problem = await _apply(message)
    text = f"The hours of the night are now in <b>{esc(name)}</b>."
    if problem:
        text += f"\n{esc(problem)}"
    await reply(message, text)
