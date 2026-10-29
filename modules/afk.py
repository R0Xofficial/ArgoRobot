"""Tell people you are away"""

import re
import time
from contextlib import suppress
from datetime import datetime, timezone
from typing import Any

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import LinkPreviewOptions, Message, ReplyParameters

from config import settings
from core import afk, immunity, info_hooks
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, format_duration
from core.text import esc, truncate, user_link

# The name of this module's button in /help ("Afk" would look wrong).
HELP_TITLE = "AFK"

# Mark yourself away from keyboard. Anyone who mentions or replies to you while you're
# AFK gets a notice with your reason and the time you went AFK. To add a photo, GIF or
# video, send it with /afk <reason> as the caption, or reply to it with /afk. It is
# shown with your AFK notice.
ABOUT = """
Mark yourself away from keyboard. Anyone who mentions or replies to you while you're AFK gets a notice with your reason and the time you went AFK, so nobody waits for an answer that isn't coming.

Your next message ends your AFK and I tell you how long you were away. The same person is told about you at most once a minute per chat, so the chat doesn't fill with notices. AFK follows you into every chat I share with you.

To add a photo, GIF or video, send it with /afk <reason> as the caption, or reply to it with /afk. It is shown with your AFK notice.
"""

HELP = [
    ("/afk [reason]", "Mark yourself AFK, with an optional reason (up to 200 characters)."),
    ("brb [reason]", "Plain-text trigger, no slash needed: same as /afk. Write it at the start of a message."),
]

router = Router(name="afk")

# Look at messages before the other modules do, so that a person who is back is noticed
# even when their message is a command another module answers.
PRIORITY = -50

NO_PREVIEW = LinkPreviewOptions(is_disabled=True)
NOTICE_COOLDOWN = 60.0  # seconds before the same person is announced again in the same chat
CACHE_LIMIT = 5000

_BRB = re.compile(r"^brb(?:\s+(.*))?$", re.IGNORECASE | re.DOTALL)
_AFK = re.compile(
    r"^[" + re.escape("".join(settings.COMMAND_PREFIXES)) + r"]afk(?:@\w+)?(?:\s|$)", re.IGNORECASE
)

_announced: dict[tuple[int, int], float] = {}


# --- Helpers ------------------------------------------------------------------

def _text_of(message: Message) -> str:
    return (message.text or message.caption or "").strip()


def _sender(message: Message):
    """The person who wrote the message, or None for channels, anonymous admins and bots."""
    user = message.from_user
    if (
        user is None
        or user.is_bot
        or message.sender_chat is not None
        or immunity.is_service_account(user.id)
    ):
        return None
    return user


def _clean_reason(text: str | None) -> str:
    return truncate(" ".join((text or "").split()), afk.MAX_REASON)


def _media_of(message: Message | None) -> tuple[str, str] | None:
    """(type, file_id) of a photo, GIF or video in the message."""
    if message is None:
        return None
    if getattr(message, "animation", None):  # a GIF also counts as a document
        return "animation", message.animation.file_id
    if getattr(message, "video", None):
        return "video", message.video.file_id
    if getattr(message, "photo", None):
        return "photo", message.photo[-1].file_id  # the biggest size comes last
    return None


def _ago(since: datetime) -> str:
    text = format_duration(datetime.now(timezone.utc) - since)
    return text or "less than a minute"


def _since_text(since: datetime) -> str:
    return since.astimezone(timezone.utc).strftime("%d %b %Y, %H:%M UTC")


def _slice(text: str, offset: int, length: int) -> str:
    """Telegram counts entity positions in UTF-16 units, Python counts characters."""
    data = text.encode("utf-16-le")
    return data[offset * 2 : (offset + length) * 2].decode("utf-16-le", errors="ignore")


def _thread(message: Message) -> int | None:
    return message.message_thread_id if message.is_topic_message else None


async def _send(message: Message, text: str, media: tuple[str, str] | None = None) -> None:
    """Answer in the same chat and topic, with the person's photo/GIF/video if they added one."""
    if media is not None:
        method, kwarg = {
            "photo": ("send_photo", "photo"),
            "animation": ("send_animation", "animation"),
            "video": ("send_video", "video"),
        }[media[0]]
        try:
            await getattr(message.bot, method)(
                chat_id=message.chat.id,
                **{kwarg: media[1]},
                caption=text,
                message_thread_id=_thread(message),
                reply_parameters=ReplyParameters(message_id=message.message_id, allow_sending_without_reply=True),
            )
            return
        except TelegramAPIError:
            pass  # the file is gone or the caption was rejected: send the text alone
    await reply(message, text, link_preview_options=NO_PREVIEW)


# --- Going AFK ----------------------------------------------------------------

async def _go_afk(message: Message, reason: str | None) -> None:
    user = _sender(message)
    if user is None:
        if message.chat.type != "private":
            await reply(message, "Anonymous senders and channels can't go AFK. Write as yourself first.")
        return
    media = _media_of(message) or _media_of(message.reply_to_message)
    reason = _clean_reason(reason)
    await afk.set_away(
        user.id,
        user.full_name,
        user.username,
        reason,
        media[0] if media else None,
        media[1] if media else None,
    )
    text = f"{user_link(user.id, user.full_name)} is now <b>AFK</b>!"
    if reason:
        text += f"\nReason: {esc(reason)}"
    await reply(message, text, link_preview_options=NO_PREVIEW)


@router.message(cmd("afk"))
async def cmd_afk(message: Message, command: CommandObject) -> None:
    await _go_afk(message, command.args)


async def _brb(message: Message) -> dict[str, Any] | bool:
    match = _BRB.match(_text_of(message))
    return {"brb_reason": match.group(1) or ""} if match else False


@router.message(_brb)
async def brb(message: Message, brb_reason: str) -> None:
    await _go_afk(message, brb_reason)


# --- Coming back, and announcing people who are away ----------------------------

def _wants_afk(message: Message) -> bool:
    text = _text_of(message)
    return bool(_AFK.match(text) or _BRB.match(text))


async def _afk_users_in(message: Message) -> list[dict[str, Any]]:
    """People who are away and were mentioned in, or replied to by, the message."""
    found: dict[int, dict[str, Any]] = {}

    def add(record: dict[str, Any] | None) -> None:
        if record is not None:
            found.setdefault(record["user_id"], record)

    replied = message.reply_to_message
    if replied is not None and not getattr(replied, "is_automatic_forward", False):
        if replied.from_user is not None and replied.sender_chat is None:
            add(await afk.get(replied.from_user.id))

    for text, entities in ((message.text, message.entities), (message.caption, message.caption_entities)):
        for entity in entities or []:
            kind = getattr(entity.type, "value", entity.type)
            if kind == "text_mention" and entity.user is not None:
                add(await afk.get(entity.user.id))
            elif kind == "mention" and text:
                add(await afk.by_username(_slice(text, entity.offset, entity.length)))
    return list(found.values())


def _recently_announced(chat_id: int, user_id: int) -> bool:
    now = time.monotonic()
    if len(_announced) >= CACHE_LIMIT:
        _announced.clear()
    last = _announced.get((chat_id, user_id))
    if last is not None and now - last < NOTICE_COOLDOWN:
        return True
    _announced[(chat_id, user_id)] = now
    return False


def _notice(record: dict[str, Any]) -> str:
    text = f"{user_link(record['user_id'], record['name'])} is <b>AFK</b>!"
    if record["reason"]:
        text += f"\nReason: {esc(record['reason'])}"
    text += f"\nAway since: {_since_text(record['since'])} ({_ago(record['since'])} ago)"
    return text


async def _watch(message: Message) -> bool:
    """
    Look at every message without taking it: the event goes on to the other modules.
    Always returns False, so no handler is attached to it.
    """
    if _wants_afk(message):
        return False
    user = _sender(message)
    if user is None:
        return False

    try:
        if not (getattr(message, "new_chat_members", None) or getattr(message, "left_chat_member", None)):
            back = await afk.clear(user.id)
            if back is not None:
                await reply(
                    message,
                    f"{user_link(user.id, user.full_name)} is no longer <b>AFK</b>! "
                    f"You were away for {_ago(back['since'])}.",
                    link_preview_options=NO_PREVIEW,
                )

        if message.chat.type in GROUP_TYPES and await afk.anyone_away():
            for record in await _afk_users_in(message):
                if record["user_id"] == user.id:
                    continue
                if _recently_announced(message.chat.id, record["user_id"]):
                    continue
                media = (record["media_type"], record["media_file_id"]) if record["media_type"] else None
                await _send(message, _notice(record), media)
    except TelegramAPIError:
        pass  # a message we can't answer (deleted, no rights to write) is not worth an error
    return False


# Registered after the commands above: /afk and brb are handled first, everything else
# only passes through here.
@router.message(_watch)
async def _never(message: Message) -> None:
    """Not reached: _watch never matches, it only looks."""


# --- /info ------------------------------------------------------------------------

async def _info_flags(bot: Any, user_id: int, chat_id: int | None) -> list[tuple[str, bool]]:
    return [("AFK", await afk.get(user_id) is not None)]


info_hooks.register("afk", _info_flags)
