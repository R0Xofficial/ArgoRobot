"""Log channel: a copy of a chat's moderation actions in a chat of the admins' choice"""

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import chatlog, moderation
from core.filters import cmd
from core.helpers import reply
from core.roles import Role
from core.text import esc

ABOUT = """
Every ban, mute, warn, promotion and lock in this chat is written, with who did it and why, into a log chat you choose: a private group of the admins, or a channel. It keeps a history that outlives the messages in the chat.

To set it up, add me to the log chat as an admin (in a channel I need the right to post), then send here:
/setlog <chat id>
The id of a chat is shown by /id when you send it there. For a channel you can also forward any post of it here and reply to that with /setlog.

You have to be an admin of the log chat yourself. Silent commands like /sban leave no trace in the chat, but they are logged. A chat has one log.

Only admins with the right to change chat info can use these commands.
"""

HELP_WHO = "admins"

HELP = [
    ("/setlog <chat id>", "Send this chat's moderation log to that chat. You can also reply to a message forwarded from a channel."),
    ("/unsetlog", "Stop logging this chat."),
    ("/logchannel", "Show where this chat is logged."),
]

router = Router(name="logchannel")

RIGHT = "can_change_info"
LOG_TYPES = ("group", "supergroup", "channel")


def _wanted(message: Message, command: CommandObject) -> int | str | None:
    """The log chat the person named: an id, a @username, or the channel of a forwarded post."""
    word = (command.args or "").split(maxsplit=1)[0] if (command.args or "").strip() else ""
    if word.lstrip("-").isdigit():
        return int(word)
    if word.startswith("@") and len(word) > 1:
        return word
    replied = moderation.replied_message(message)
    origin = getattr(replied, "forward_origin", None) if replied is not None else None
    origin_chat = getattr(origin, "chat", None)
    return origin_chat.id if origin_chat is not None else None


async def _begin(message: Message, role: Role | None):
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return None
    return await moderation.begin_chat_command(message, role, right=RIGHT)


@router.message(cmd("setlog"))
async def cmd_setlog(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await _begin(message, role)
    if actor is None:
        return
    wanted = _wanted(message, command)
    if wanted is None:
        await reply(
            message,
            "Tell me where to log: /setlog &lt;chat id&gt;, or reply to a post forwarded from the channel. "
            "Add me to that chat first. /id shows the id of the chat it is sent in.",
        )
        return

    bot = message.bot
    try:
        target = await bot.get_chat(wanted)
    except TelegramAPIError:
        await reply(message, "I can't find that chat. Add me to it first, then try again.")
        return
    kind = getattr(target.type, "value", target.type)
    if kind not in LOG_TYPES:
        await reply(message, "The log has to be a group or a channel.")
        return
    if target.id == message.chat.id:
        await reply(message, "A chat can't be its own log. Use another chat.")
        return

    # The person must be an admin there, or anyone could point a log at someone else's chat.
    if actor.id is None or not await moderation.is_chat_admin(bot, target.id, actor.id):
        await reply(message, "You have to be an admin of the log chat yourself.")
        return

    title = esc(message.chat.title or "this chat")
    try:
        await bot.send_message(chat_id=target.id, text=f"This chat is now the log of <b>{title}</b>.")
    except TelegramAPIError:
        await reply(message, "I can't write in that chat. Make me an admin there (in a channel, with the right to post) and try again.")
        return

    await chatlog.set_log(message.chat.id, target.id, actor.id)
    await reply(message, f"Done. This chat's log goes to <b>{esc(target.title or str(target.id))}</b> from now on.")


@router.message(cmd("unsetlog"))
async def cmd_unsetlog(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await _begin(message, role) is None:
        return
    if not await chatlog.unset_log(message.chat.id):
        await reply(message, "This chat has no log.")
        return
    await reply(message, "This chat is no longer logged.")


@router.message(cmd("logchannel"))
async def cmd_logchannel(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await _begin(message, role) is None:
        return
    target = await chatlog.get(message.chat.id)
    if target is None:
        await reply(message, "This chat has no log. Set one with /setlog.")
        return
    try:
        chat = await message.bot.get_chat(target)
        name = esc(chat.title or str(target))
    except TelegramAPIError:
        name = "a chat I can no longer reach"
    await reply(message, f"This chat is logged in <b>{name}</b> (<code>{target}</code>).")
