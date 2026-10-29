"""
Saved messages: reading one from a command and sending it later.

Used by the modules that keep a message written by a chat admin and send it when
something happens (filters answer keywords, greetings welcome new members). The
message is kept as text in the dialect of core/markup.py plus an optional media file,
and is turned into a real Telegram message only when it is sent.
"""

import logging
from contextlib import suppress
from typing import Any

from aiogram.exceptions import TelegramAPIError
from aiogram.types import (
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
    ReplyParameters,
)

from core import markup
from core.helpers import reply
from core.markup import MarkupError
from core.text import esc

log = logging.getLogger(__name__)

CAPTION_LIMIT = 1024
MESSAGE_LIMIT = 4096
MAX_CONTENT = 4000

_METHODS = {
    "photo": "send_photo",
    "video": "send_video",
    "animation": "send_animation",
    "document": "send_document",
    "audio": "send_audio",
    "voice": "send_voice",
    "sticker": "send_sticker",
    "video_note": "send_video_note",
}


# --- Reading what to save --------------------------------------------------------------

def media_of(message: Message | None) -> tuple[str, str] | None:
    """(kind, file id) of the media a message carries, if any."""
    if message is None:
        return None
    if getattr(message, "animation", None):  # a GIF is also a document
        return "animation", message.animation.file_id
    for kind in ("video", "audio", "voice", "video_note", "sticker", "document"):
        found = getattr(message, kind, None)
        if found:
            return kind, found.file_id
    photo = getattr(message, "photo", None)
    if photo:
        return "photo", photo[-1].file_id  # the biggest size comes last
    return None


def text_of(message: Message) -> str:
    """The text (or caption) of a message, written back in the markup dialect."""
    if message.text:
        return markup.from_entities(message.text, message.entities)
    return markup.from_entities(message.caption or "", message.caption_entities)


def validate(text: str, has_media: bool) -> None:
    """Raise MarkupError (with a sentence for people) when this could not be sent."""
    if len(text) > MAX_CONTENT:
        raise MarkupError(f"That message is too long: at most {MAX_CONTENT} characters.")
    markup.check(text)
    for variant in markup.variants(text):
        html = markup.parse(variant).html
        if len(html) > MESSAGE_LIMIT:
            raise MarkupError("A variant is too long once formatted.")
        if not html and not has_media:
            raise MarkupError("A message needs some text (buttons alone are not enough).")


# --- Sending ---------------------------------------------------------------------------

async def fillings(message: Message, user, source: str, chat=None) -> dict[str, str]:
    """Facts about `user` for the {first}, {mention}... of a saved message (and about `chat`, default the message's)."""
    chat = chat or message.chat
    values = {
        "first": user.first_name or "",
        "last": user.last_name or "",
        "fullname": user.full_name,
        "username": f"@{user.username}" if user.username else "",
        "mention": user.full_name,
        "id": str(user.id),
        "chatname": chat.title or "",
        "count": "",
    }
    if "{count}" in source:
        with suppress(TelegramAPIError):
            values["count"] = str(await message.bot.get_chat_member_count(chat.id))
    return values


def _keyboard(parsed: markup.Parsed) -> InlineKeyboardMarkup | None:
    if not parsed.buttons:
        return None
    return InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text=text, url=url) for text, url in row] for row in parsed.buttons]
    )


async def _send_media(message: Message, kind: str, file_id: str, caption: str | None, extra: dict[str, Any]) -> Message:
    kwargs: dict[str, Any] = {
        "chat_id": message.chat.id,
        kind: file_id,
        "message_thread_id": message.message_thread_id if message.is_topic_message else None,
        "reply_parameters": ReplyParameters(message_id=message.message_id, allow_sending_without_reply=True),
        **extra,
    }
    if caption is not None and kind not in ("sticker", "video_note"):
        kwargs["caption"] = caption
    return await getattr(message.bot, _METHODS[kind])(**kwargs)


async def send(
    message: Message,
    user,
    content: str,
    media_type: str | None,
    media_file_id: str | None,
    *,
    raw: bool = False,
    label: str = "saved message",
    chat=None,
) -> Message | None:
    """
    Send a saved message in answer to `message`, filled in for `user`. Returns the
    message that was sent (the media, or the text when it follows on its own), or None
    when nothing could be sent.

    raw: send the text exactly as it was saved, without formatting, buttons or options.
    chat: the chat the message belongs to when it is sent somewhere else (a private chat).
    """
    if raw:
        parsed = markup.Parsed(esc(content), [], set())
    else:
        source = markup.pick(content)
        try:
            parsed = markup.parse(source, await fillings(message, user, source, chat))
        except MarkupError:
            log.warning("A %s in chat %s can no longer be sent", label, message.chat.id)
            return None

    keyboard = _keyboard(parsed)
    extra: dict[str, Any] = {}
    if "protect" in parsed.options:
        extra["protect_content"] = True
    if "nonotif" in parsed.options:
        extra["disable_notification"] = True
    text_extra = {
        **extra,
        "link_preview_options": LinkPreviewOptions(is_disabled="preview" not in parsed.options),
    }

    sent: Message | None = None
    text_follows = True
    if media_type:
        media_extra = dict(extra)
        if "mediaspoiler" in parsed.options and media_type in ("photo", "video", "animation"):
            media_extra["has_spoiler"] = True
        # A caption has to be short and a sticker has none: then the text follows on its own.
        inline = bool(parsed.html) and len(parsed.html) <= CAPTION_LIMIT and media_type not in ("sticker", "video_note")
        text_follows = bool(parsed.html) and not inline
        if keyboard and not text_follows:
            media_extra["reply_markup"] = keyboard  # nothing else carries the buttons
        try:
            sent = await _send_media(message, media_type, media_file_id, parsed.html if inline else None, media_extra)
        except TelegramAPIError:
            log.warning("A %s in chat %s: its media can't be sent", label, message.chat.id)
            if not parsed.html:
                return None
            text_follows = True  # fall back to the text alone
        if not text_follows:
            return sent
    try:
        text_message = await reply(message, parsed.html, reply_markup=keyboard, **text_extra)
    except TelegramAPIError:
        return sent
    return sent or text_message
