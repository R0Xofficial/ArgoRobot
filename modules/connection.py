"""Managing a group from a private chat"""

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import connection, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.text import esc
from core.i18n import tr

ABOUT = "connection.about"

HELP = [
    ("/connect [chat id or @username]", "connection.help.in_a_group_i_send", "common.admins"),
    ("/disconnect", "connection.help.stop_managing_the_group_from", "common.admins"),
    ("/connection", "connection.help.show_which_group_this_private", "common.admins"),
]

router = Router(name="connection")


def _group_name(record: dict) -> str:
    return f"<b>{esc(record['title'] or str(record['chat_id']))}</b>"


def _reference(text: str) -> int | str | None:
    """A chat id or @username from what the person typed, None when it is neither."""
    word = text.split()[0] if text.split() else ""
    if word.lstrip("-").isdigit():
        return int(word)
    name = word.lstrip("@")
    return f"@{name}" if name and name.replace("_", "").isalnum() else None


async def connect_and_tell(message: Message, reference: int | str) -> None:
    """Connect the sender to a group and tell them how it went."""
    try:
        record = await connection.connect(message.bot, message.from_user.id, reference)
    except connection.ConnectError as exc:
        await reply(message, str(exc))
        return
    await reply(
        message,
        tr("connection.you_are_connected_to_its_admin", group_name=_group_name(record)),
    )


@router.message(cmd("connect"))
async def cmd_connect(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type in GROUP_TYPES:
        if await moderation.begin_chat_command(message, role, right="can_manage_chat") is None:
            return
        me = await message.bot.me()
        button = InlineKeyboardButton(text=tr("connection.connect"), url=f"https://t.me/{me.username}?start=connect_{message.chat.id}")
        await reply(
            message,
            tr("connection.tap_the_button_to_connect_your"),
            reply_markup=InlineKeyboardMarkup(inline_keyboard=[[button]]),
        )
        return
    if message.chat.type != "private":
        await reply(message, tr("connection.use_this_in_a_group_or"))
        return
    argument = (command.args or "").strip()
    if not argument:
        record = await connection.get(message.from_user.id)
        if record is not None:
            await reply(message, tr("connection.you_are_connected_to_use_disconnect", group_name=_group_name(record)))
        else:
            await reply(message, tr("connection.use_connect_lt_chat_id_or"))
        return
    reference = _reference(argument)
    if reference is None:
        await reply(message, tr("connection.give_me_the_group_s_id"))
        return
    await connect_and_tell(message, reference)


@router.message(cmd("disconnect"))
async def cmd_disconnect(message: Message) -> None:
    if message.chat.type != "private":
        await reply(message, tr("connection.send_this_to_me_in_a"))
        return
    record = await connection.get(message.from_user.id)
    if record is None or not await connection.clear(message.from_user.id):
        await reply(message, tr("connection.you_are_not_connected_to_a"))
        return
    await reply(message, tr("connection.you_are_no_longer_connected_to", group_name=_group_name(record)))


@router.message(cmd("connection"))
async def cmd_connection(message: Message) -> None:
    if message.chat.type != "private":
        await reply(message, tr("connection.send_this_to_me_in_a"))
        return
    record = await connection.get(message.from_user.id)
    if record is None:
        await reply(message, tr("connection.you_are_not_connected_to_a_2"))
        return
    await reply(message, tr("connection.you_are_connected_to", group_name=_group_name(record), chat_id=record['chat_id']))
