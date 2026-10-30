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

ABOUT = """
Admins can run a group from a private chat with me, so the group stays free of commands.

Connect with /connect in the group (I send a button) or /connect <chat id or @username> here. From then on the group's admin commands sent to me privately, like /ban @someone or /lock url, act on that group, and I answer privately. Your rights in the group are checked every time, exactly as if you had sent the command there.

Commands that need a reply to a message, that delete your command or that show a saved message in the chat don't work this way: send those in the group. My own commands, like /help, are answered normally.
"""

HELP = [
    ("/connect [chat id or @username]", "In a group, I send a button that connects your private chat with me to it. In a private chat, give the group's ID or @username. Only admins of the group can connect to it.", "admins"),
    ("/disconnect", "Stop managing the group from this private chat.", "admins"),
    ("/connection", "Show which group this private chat is connected to.", "admins"),
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
        f"You are connected to {_group_name(record)}. Its admin commands you send me here now act on it.\n"
        "Use /disconnect to stop.",
    )


@router.message(cmd("connect"))
async def cmd_connect(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type in GROUP_TYPES:
        if await moderation.begin_chat_command(message, role, allow_staff=False, right="can_manage_chat") is None:
            return
        me = await message.bot.me()
        button = InlineKeyboardButton(text="Connect", url=f"https://t.me/{me.username}?start=connect_{message.chat.id}")
        await reply(
            message,
            "Tap the button to connect your private chat with me to this group. Only admins can.",
            reply_markup=InlineKeyboardMarkup(inline_keyboard=[[button]]),
        )
        return
    if message.chat.type != "private":
        await reply(message, "Use this in a group or in a private chat with me.")
        return
    argument = (command.args or "").strip()
    if not argument:
        record = await connection.get(message.from_user.id)
        if record is not None:
            await reply(message, f"You are connected to {_group_name(record)}. Use /disconnect to stop.")
        else:
            await reply(message, "Use /connect &lt;chat id or @username&gt;, or send /connect in the group.")
        return
    reference = _reference(argument)
    if reference is None:
        await reply(message, "Give me the group's ID or @username.")
        return
    await connect_and_tell(message, reference)


@router.message(cmd("disconnect"))
async def cmd_disconnect(message: Message) -> None:
    if message.chat.type != "private":
        await reply(message, "Send this to me in a private chat.")
        return
    record = await connection.get(message.from_user.id)
    if record is None or not await connection.clear(message.from_user.id):
        await reply(message, "You are not connected to a group.")
        return
    await reply(message, f"You are no longer connected to {_group_name(record)}.")


@router.message(cmd("connection"))
async def cmd_connection(message: Message) -> None:
    if message.chat.type != "private":
        await reply(message, "Send this to me in a private chat.")
        return
    record = await connection.get(message.from_user.id)
    if record is None:
        await reply(message, "You are not connected to a group. Use /connect.")
        return
    await reply(message, f"You are connected to {_group_name(record)} (<code>{record['chat_id']}</code>).")
