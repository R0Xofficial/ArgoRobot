"""
Sending helpers.

Handlers should answer through these functions instead of calling
message.answer() / message.reply() directly, because they take care of the
things Telegram likes to break:

  * Topics: the answer always goes to the topic the command was sent in.
  * Deleted messages: if the user deletes their command while the bot is still
    working, the answer is still delivered to the same chat and topic, it just
    is not attached to the vanished message.
  * Broken markup: if Telegram rejects the HTML ("can't parse entities"), the
    text is resent as plain text instead of being lost.
"""

import logging
from typing import Any

from aiogram.exceptions import TelegramBadRequest
from aiogram.types import Message, ReplyParameters

from core import connection
from core.text import strip_tags

log = logging.getLogger(__name__)

_MAX_ATTEMPTS = 3


def _is_parse_error(problem: str) -> bool:
    return "can't parse entities" in problem


def _is_missing_reply_target(problem: str) -> bool:
    return "repl" in problem and "not found" in problem


async def reply(
    message: Message,
    text: str,
    *,
    quote: bool | None = None,
    **kwargs: Any,
) -> Message:
    """
    Send `text` where `message` was sent: same chat, same topic.

    quote: attach the answer to the original message. None (default) means
           "only in groups", private chats stay clean.
    kwargs: forwarded to Bot.send_message (reply_markup, parse_mode, ...).
    """
    if quote is None:
        quote = message.chat.type != "private"

    params: dict[str, Any] = {
        "chat_id": message.chat.id,
        "text": text,
        # General topic and non-forum threads must not receive a thread id.
        "message_thread_id": message.message_thread_id if message.is_topic_message else None,
        **kwargs,
    }
    private = connection.origin.get()
    if private is not None:
        # A command run for a connected group (core/connection.py) is answered where it was sent.
        params["chat_id"], params["message_thread_id"], quote = private, None, False
    if quote:
        # allow_sending_without_reply makes Telegram deliver the answer even
        # if the original message has been deleted in the meantime.
        params["reply_parameters"] = ReplyParameters(
            message_id=message.message_id,
            allow_sending_without_reply=True,
        )

    bot = message.bot
    for _ in range(_MAX_ATTEMPTS - 1):
        try:
            return await bot.send_message(**params)
        except TelegramBadRequest as exc:
            problem = str(exc).lower()
            if _is_parse_error(problem) and params.get("parse_mode", "default") is not None:
                log.warning("Telegram rejected the markup, resending as plain text")
                params["text"] = strip_tags(params["text"])
                params["parse_mode"] = None
            elif _is_missing_reply_target(problem) and "reply_parameters" in params:
                params.pop("reply_parameters")
            else:
                raise
    return await bot.send_message(**params)


async def edit(message: Message, text: str, **kwargs: Any) -> Message | bool | None:
    """
    Edit a message the bot sent.

    Returns None when there was nothing to change ("message is not modified").
    Falls back to plain text if Telegram rejects the markup.
    """
    try:
        return await message.edit_text(text, **kwargs)
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if "message is not modified" in problem:
            return None
        if _is_parse_error(problem) and kwargs.get("parse_mode", "default") is not None:
            log.warning("Telegram rejected the markup, editing as plain text")
            kwargs["parse_mode"] = None
            return await message.edit_text(strip_tags(text), **kwargs)
        raise
