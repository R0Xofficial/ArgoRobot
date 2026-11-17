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
from core.i18n import tr

ABOUT = "nightmode.about"

HELP_WHO = "common.admins"

HELP = [
    ("/nightmode [on|off]", "nightmode.help.turn_nightmode_on_or_off"),
    ("/nighthours <from> <to>", "nightmode.help.when_the_night_starts_and"),
    ("/nighttz <zone>", "nightmode.help.the_time_zone_of_those"),
]

router = Router(name="nightmode")


def _summary(settings: nightmode.Settings) -> str:
    night = settings.enabled and nightmode.is_night(settings)
    now = datetime.now(timezone.utc).astimezone(nightmode.zone_of(settings.zone))
    return (
        tr("nightmode.nightmode_enabled_night_to_time_zone", opt=tr("common.on") if settings.enabled else tr("common.off"), clock_text=nightmode.clock_text(settings.start), clock_text2=nightmode.clock_text(settings.end), zone=esc(settings.zone), now=now, opt2=tr("nightmode.closed") if settings.active else tr("nightmode.open"))
        + ("" if not (night and not settings.active) else tr("nightmode.the_night_has_begun_i_close"))
    )


async def _allowed(message: Message, role: Role | None) -> bool:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return False
    return await moderation.begin_chat_command(message, role) is not None


async def _apply(message: Message) -> str | None:
    """Close or open the chat now when the clock says so; the sweep would do it within half a minute anyway."""
    try:
        what = await nightmode.reconcile(message.bot, message.chat.id)
    except ModerationError as exc:
        return str(exc)
    except Exception as exc:
        return tr("nightmode.i_can_t_change_the_permissions", getattr=getattr(exc, 'message', exc))
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
            await reply(message, tr("nightmode.use_nightmode_on_or_nightmode_off"))
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
            tr("nightmode.use_nighthours_lt_from_gt_lt"),
        )
        return
    await nightmode.update(message.chat.id, start=start, end=end)
    problem = await _apply(message)
    text = (
        tr("nightmode.the_night_now_lasts_from_to", clock_text=nightmode.clock_text(start), clock_text2=nightmode.clock_text(end), zone=esc((await nightmode.get(message.chat.id)).zone))
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
            tr("nightmode.use_nighttz_lt_zone_gt_a"),
        )
        return
    name, _ = parsed
    await nightmode.update(message.chat.id, zone=name)
    problem = await _apply(message)
    text = tr("nightmode.the_hours_of_the_night_are", name=esc(name))
    if problem:
        text += f"\n{esc(problem)}"
    await reply(message, text)
