"""
Finding out which user a command is aimed at.

    /addsudo                  (as a reply to the person's message)
    /addsudo 123456789        (user id)
    /addsudo @username        (only works if Telegram lets the bot look it up)
    /addsudo <mention>        (a mention of a user without a username)
"""

import logging
import re
from dataclasses import dataclass

from aiogram import Bot
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

log = logging.getLogger(__name__)

_USERNAME_RE = re.compile(r"^@?([A-Za-z][A-Za-z0-9_]{3,31})$")


class TargetError(Exception):
    """Raised with a message that is safe to show to the user (plain text)."""


@dataclass
class Target:
    user_id: int
    name: str | None


async def display_name(bot: Bot, user_id: int, stored: str | None = None) -> str | None:
    """Current name of a user, or the stored one when Telegram will not say."""
    try:
        chat = await bot.get_chat(user_id)
        return chat.full_name or stored
    except TelegramAPIError:
        return stored


async def resolve_target(message: Message, command: CommandObject) -> Target:
    """Return the user a command refers to, or raise TargetError."""
    bot = message.bot
    args = (command.args or "").split()

    if args:
        first = args[0]

        # A mention of a user without a username carries the user inside.
        for entity in message.entities or []:
            if entity.type == "text_mention" and entity.user is not None:
                return _from_user(entity.user)

        if first.isdigit():
            user_id = int(first)
            if user_id <= 0:
                raise TargetError("That is not a valid user id.")
            return Target(user_id, await display_name(bot, user_id))

        match = _USERNAME_RE.match(first)
        if match:
            try:
                chat = await bot.get_chat(f"@{match.group(1)}")
            except TelegramAPIError:
                raise TargetError(
                    f"I can't find @{match.group(1)}. Reply to their message or use their id."
                ) from None
            if getattr(chat.type, "value", chat.type) != "private":
                raise TargetError("That is not a user.")
            return Target(chat.id, chat.full_name)

        raise TargetError("Reply to a user's message or give a user id or @username.")

    reply = message.reply_to_message
    # In forum topics every message "replies" to the service message that
    # created the topic. That is not a person the command is aimed at.
    if reply is not None and getattr(reply, "forum_topic_created", None) is not None:
        reply = None

    if reply is None:
        raise TargetError("Reply to a user's message or give a user id or @username.")
    if reply.from_user is None:
        raise TargetError("I can't tell who wrote that message.")
    return _from_user(reply.from_user)


def _from_user(user) -> Target:
    if getattr(user, "is_bot", False):
        raise TargetError("Bots cannot hold roles.")
    return Target(user.id, user.full_name)
