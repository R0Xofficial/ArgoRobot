"""Chat rules"""

import logging
import re
from typing import Any

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import moderation, rulestore, sender
from core.filters import cmd
from core.helpers import reply
from core.markup import MarkupError
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.targets import replied_message
from core.text import esc
from core.i18n import tr

log = logging.getLogger(__name__)

ABOUT = "rules.about"

HELP = [
    ("/rules", "rules.help.get_the_rules_of_this"),
    ("/rules here", "rules.help.show_the_rules_in_the"),
    ("/rules noformat", "rules.help.show_the_rules_in_the_2"),
    ("/setrules <reply or text>", "rules.help.set_the_rules_reply_to", "common.admins"),
    ("/resetrules", "rules.help.clear_the_rules_of_this", "common.admins"),
]

HELP_NOTES = [
    (
        "common.formatting",
        "common.bold_italic_underline_strike_spoiler_code",
    ),
    (
        "common.fillings",
        "rules.note.replaced_by_facts_about_whoever",
    ),
    (
        "common.options",
        "rules.note.put_these_anywhere_in_the",
    ),
    (
        "common.buttons",
        "rules.note.text_buttonurl_example_com_a",
    ),
    (
        "common.random_content",
        "common.separate_variants_with_and_i_send",
    ),
    (
        "common.examples",
        "rules.note.setrules_1_be_kind_2",
    ),
]

router = Router(name="rules")

# Before the module that answers a plain /start, so the rules link reaches us first.
PRIORITY = -10

RIGHT = "can_change_info"
_DEEP_LINK = re.compile(r"^/start(?:@\w+)?\s+rules_(-?\d+)\s*$", re.IGNORECASE)
_LEFT = {"left", "kicked"}


# --- Setting ----------------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """The chat's own business: admins only, never the bot's staff."""
    return await moderation.begin_chat_command(message, role, right=RIGHT) is not None


@router.message(cmd("setrules"))
async def cmd_setrules(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role):
        return
    text = sender.args_of(message, command)
    replied = replied_message(message)
    media = sender.media_of(message) or sender.media_of(replied)  # media sent with the command, else the replied-to message's
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, tr("rules.tell_me_what_the_rules_are"))
        return
    try:
        sender.validate(text, media is not None)
    except MarkupError as exc:
        await reply(message, esc(exc))
        return
    await rulestore.save(message.chat.id, text, media[0] if media else None, media[1] if media else None)
    await reply(message, tr("rules.the_rules_are_saved_use_rules"))


@router.message(cmd("resetrules"))
async def cmd_resetrules(message: Message, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role):
        return
    if await rulestore.remove(message.chat.id):
        await reply(message, tr("rules.the_rules_are_cleared"))
    else:
        await reply(message, tr("rules.this_chat_has_no_rules_to"))


# --- Asking -----------------------------------------------------------------------------------

async def _send_rules(message: Message, chat: Any, *, raw: bool = False) -> bool:
    """Send the rules of `chat` where `message` was sent. False when the chat has none."""
    row = await rulestore.get(chat.id)
    if row is None or (not row["content"] and not row["media_type"]):
        return False
    await sender.send(
        message, message.from_user, row["content"], row["media_type"], row["media_file_id"],
        raw=raw, label=tr("rules.rules"), chat=chat,
    )
    return True


@router.message(cmd("rules"))
async def cmd_rules(message: Message, command: CommandObject) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    argument = (command.args or "").strip().lower()
    if argument not in ("", "here", "noformat"):
        await reply(message, tr("rules.use_rules_rules_here_or_rules"))
        return
    row = await rulestore.get(message.chat.id)
    if row is None or (not row["content"] and not row["media_type"]):
        await reply(message, tr("rules.this_chat_has_no_rules_yet"))
        return
    if argument:
        await _send_rules(message, message.chat, raw=argument == "noformat")
        return
    me = await message.bot.me()
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text=tr("rules.rules_2"), url=f"https://t.me/{me.username}?start=rules_{message.chat.id}")]]
    )
    await reply(message, tr("rules.click_the_button_below_to_read"), reply_markup=keyboard)


async def _opened_link(message: Message) -> dict[str, Any] | bool:
    """A private /start that carries the rules link made by /rules."""
    if message.chat.type != "private" or message.from_user is None or not message.text:
        return False
    found = _DEEP_LINK.match(message.text)
    return {"rules_chat": int(found.group(1))} if found else False


@router.message(_opened_link)
async def on_link(message: Message, rules_chat: int) -> None:
    try:
        chat = await message.bot.get_chat(rules_chat)
    except TelegramAPIError:
        await reply(message, tr("rules.i_can_t_find_that_chat"))
        return
    status = await moderation.member_status(message.bot, rules_chat, message.from_user.id)
    if chat.type not in GROUP_TYPES or status is None or status in _LEFT:
        await reply(message, tr("rules.you_are_not_in_that_chat"))
        return
    if not await _send_rules(message, chat):
        await reply(message, tr("rules.has_no_rules_yet", v=esc(chat.title or tr("rules.that_chat"))))
