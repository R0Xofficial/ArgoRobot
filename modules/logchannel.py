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
from core.i18n import tr

# The name of this module's button in /help.
HELP_TITLE = "logchannel.title"

ABOUT = "logchannel.about"

HELP_WHO = "common.admins"

HELP = [
    ("/setlog <chat id>", "logchannel.help.send_this_chat_s_moderation"),
    ("/unsetlog", "logchannel.help.stop_logging_this_chat"),
    ("/logchannel", "logchannel.help.show_where_this_chat_is"),
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
        await reply(message, tr("common.this_command_only_works_in_groups"))
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
            tr("logchannel.tell_me_where_to_log_setlog"),
        )
        return

    bot = message.bot
    try:
        target = await bot.get_chat(wanted)
    except TelegramAPIError:
        await reply(message, tr("logchannel.i_can_t_find_that_chat"))
        return
    kind = getattr(target.type, "value", target.type)
    if kind not in LOG_TYPES:
        await reply(message, tr("logchannel.the_log_has_to_be_a"))
        return
    if target.id == message.chat.id:
        await reply(message, tr("logchannel.a_chat_can_t_be_its"))
        return

    # The person must be an admin there, or anyone could point a log at someone else's chat.
    if actor.id is None or not await moderation.is_chat_admin(bot, target.id, actor.id):
        await reply(message, tr("logchannel.you_have_to_be_an_admin"))
        return

    title = esc(message.chat.title or tr("common.this_chat"))
    try:
        await bot.send_message(chat_id=target.id, text=tr("logchannel.this_chat_is_now_the_log", title=title))
    except TelegramAPIError:
        await reply(message, tr("logchannel.i_can_t_write_in_that"))
        return

    await chatlog.set_log(message.chat.id, target.id, actor.id)
    await reply(message, tr("logchannel.done_this_chat_s_log_goes", v=esc(target.title or str(target.id))))


@router.message(cmd("unsetlog"))
async def cmd_unsetlog(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await _begin(message, role) is None:
        return
    if not await chatlog.unset_log(message.chat.id):
        await reply(message, tr("logchannel.this_chat_has_no_log"))
        return
    await reply(message, tr("logchannel.this_chat_is_no_longer_logged"))


@router.message(cmd("logchannel"))
async def cmd_logchannel(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if await _begin(message, role) is None:
        return
    target = await chatlog.get(message.chat.id)
    if target is None:
        await reply(message, tr("logchannel.this_chat_has_no_log_set"))
        return
    try:
        chat = await message.bot.get_chat(target)
        name = esc(chat.title or str(target))
    except TelegramAPIError:
        name = tr("logchannel.a_chat_i_can_no_longer")
    await reply(message, tr("logchannel.this_chat_is_logged_in", name=name, target=target))
