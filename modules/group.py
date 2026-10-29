"""Group administration"""

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import chatsettings, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError
from core.roles import Role
from core.text import esc

HELP = [
    ("/staffhelp [on|off|yes|no]", "Let the bot's staff help moderate this chat, or not (chat owner only)"),
]

router = Router(name="group")

_ANSWERS = {"on": True, "yes": True, "off": False, "no": False}


def _state(enabled: bool) -> str:
    return "on" if enabled else "off"


@router.message(cmd("staffhelp"))
async def cmd_staffhelp(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """
    Whether the bot's staff (Owner, Developers, Sudo) may help moderate this chat.

    On by default. Only the chat's owner can change it; any admin can look.
    When it is off, the staff's commands are treated like those of any other member.
    Either way the staff never changes the chat's settings.
    """
    argument = (command.args or "").strip().lower()

    if not argument:
        # Looking at a setting is for the chat's own admins, never for the staff.
        if await moderation.begin_chat_command(message, role, allow_staff=False) is None:
            return
        enabled = await chatsettings.staff_help(message.chat.id)
        await reply(
            message,
            f"Help from the bot's staff is <b>{_state(enabled)}</b> in this chat.\n"
            "The chat owner can change it with /staffhelp on or /staffhelp off.",
        )
        return

    if argument not in _ANSWERS:
        await reply(message, "Use /staffhelp on or /staffhelp off (yes and no work too).")
        return

    try:
        await _require_chat_owner(message)
    except ModerationError as exc:
        await reply(message, esc(exc))
        return

    enabled = _ANSWERS[argument]
    await chatsettings.set_staff_help(message.chat.id, enabled)
    if enabled:
        text = (
            "Help from the bot's staff is now <b>on</b>. Developers and Sudo users can help "
            "moderate users here. Their actions are marked as taken by the bot's staff, "
            "and they can't change this chat's settings."
        )
    else:
        text = (
            "Help from the bot's staff is now <b>off</b>. Their commands are treated like "
            "those of any other member."
        )
    await reply(message, text)


async def _require_chat_owner(message: Message) -> None:
    if message.chat.type not in moderation.GROUP_TYPES:
        raise ModerationError("This command only works in groups.")
    if moderation.actor_of(message).anonymous:
        raise ModerationError("Send this command as yourself, not as an anonymous admin.")
    status = await moderation.member_status(message.bot, message.chat.id, message.from_user.id)
    if status is None:
        raise ModerationError("I couldn't check your rights in this chat.")
    if status != "creator":
        raise ModerationError("Only the owner of this chat can change this.")
