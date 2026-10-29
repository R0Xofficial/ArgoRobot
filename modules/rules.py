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

log = logging.getLogger(__name__)

ABOUT = """
Every chat can have rules that anyone can ask for. By default /rules gives you a button that opens them in a private chat with me, which keeps the group quiet; /rules here shows them in the group instead.

The rules can be text, or any media with text as its caption. Write them after /setrules, or reply to a message to use it as it is. Setting and clearing them is for chat admins with the right to change chat info.

Formatting, fillings, buttons and random variants work as described below.
"""

HELP = [
    ("/rules", "Get the rules of this chat in a private chat with me."),
    ("/rules here", "Show the rules in the group."),
    ("/rules noformat", "Show the rules in the group exactly as they were written, without formatting."),
    ("/setrules <reply or text>", "Set the rules. Reply to a message to use it, media included. Setting them again replaces the old ones.", "admins"),
    ("/resetrules", "Clear the rules of this chat.", "admins"),
]

HELP_NOTES = [
    (
        "Markdown",
        """
        *bold*  _italic_  __underline__  ~strike~  ||spoiler||
        `code`  ```pre```  [text](https://link)
        A backslash makes a character literal, like \\* or \\_. Markers only work at the edge of words, so snake_case is safe.
        """,
    ),
    (
        "Fillings",
        """
        Replaced by facts about whoever asks for the rules:
        {first} {last} {fullname} {username} {mention} {id} {chatname} {count}
        {username} falls back to a mention for people without one. {count} is the number of members.
        """,
    ),
    (
        "Options",
        """
        Put these anywhere in the text; they are removed and change how the rules are sent:
        {preview} - show link previews.
        {protect} - forbid forwarding and saving.
        {nonotif} - send silently.
        {mediaspoiler} - hide a photo, GIF or video behind a spoiler.
        """,
    ),
    (
        "Buttons",
        """
        [Text](buttonurl://example.com) - a button under the rules, on its own row.
        [Text](buttonurl://example.com:same) - a button on the row of the previous one.
        """,
    ),
    (
        "Random content",
        """
        Separate variants with %%% and I send one of them at random each time.
        """,
    ),
    (
        "Examples",
        """
        /setrules *1.* Be kind, {first}.
        *2.* No spam. [More](buttonurl://example.com/rules)
        /setrules - reply to a message to make it the rules.
        /rules noformat
        """,
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
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=RIGHT) is not None


@router.message(cmd("setrules"))
async def cmd_setrules(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role):
        return
    text = (command.args or "").strip()
    replied = replied_message(message)
    media = sender.media_of(replied)
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, "Tell me what the rules are: write them after /setrules, or reply to a message.")
        return
    try:
        sender.validate(text, media is not None)
    except MarkupError as exc:
        await reply(message, esc(exc))
        return
    await rulestore.save(message.chat.id, text, media[0] if media else None, media[1] if media else None)
    await reply(message, "The rules are saved. Use /rules to see them.")


@router.message(cmd("resetrules"))
async def cmd_resetrules(message: Message, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role):
        return
    if await rulestore.remove(message.chat.id):
        await reply(message, "The rules are cleared.")
    else:
        await reply(message, "This chat has no rules to clear.")


# --- Asking -----------------------------------------------------------------------------------

async def _send_rules(message: Message, chat: Any, *, raw: bool = False) -> bool:
    """Send the rules of `chat` where `message` was sent. False when the chat has none."""
    row = await rulestore.get(chat.id)
    if row is None or (not row["content"] and not row["media_type"]):
        return False
    await sender.send(
        message, message.from_user, row["content"], row["media_type"], row["media_file_id"],
        raw=raw, label="rules", chat=chat,
    )
    return True


@router.message(cmd("rules"))
async def cmd_rules(message: Message, command: CommandObject) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    argument = (command.args or "").strip().lower()
    if argument not in ("", "here", "noformat"):
        await reply(message, "Use /rules, /rules here or /rules noformat.")
        return
    row = await rulestore.get(message.chat.id)
    if row is None or (not row["content"] and not row["media_type"]):
        await reply(message, "This chat has no rules yet. Admins can set them with /setrules.")
        return
    if argument:
        await _send_rules(message, message.chat, raw=argument == "noformat")
        return
    me = await message.bot.me()
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text="Rules", url=f"https://t.me/{me.username}?start=rules_{message.chat.id}")]]
    )
    await reply(message, "Click the button below to read the rules of this chat.", reply_markup=keyboard)


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
        await reply(message, "I can't find that chat. Maybe I was removed from it.")
        return
    status = await moderation.member_status(message.bot, rules_chat, message.from_user.id)
    if chat.type not in GROUP_TYPES or status is None or status in _LEFT:
        await reply(message, "You are not in that chat, so I can't show you its rules.")
        return
    if not await _send_rules(message, chat):
        await reply(message, f"<b>{esc(chat.title or 'That chat')}</b> has no rules yet.")
