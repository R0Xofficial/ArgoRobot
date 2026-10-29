"""Sticker tools"""

import asyncio
import io
import logging
import re
import time

from aiogram import Router
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.filters import CommandObject
from aiogram.types import (
    BufferedInputFile,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    InputSticker,
    Message,
    ReplyParameters,
)

from core.filters import cmd
from core.helpers import reply
from core.targets import replied_message
from core.text import esc

log = logging.getLogger(__name__)

ABOUT = """
Two tools for stickers. Both work in groups and in private chat, and both need you to reply to what they should act on.

/getsticker gives you a sticker as a PNG file. Only still stickers can become images: animated and video stickers are sent as the file they are.

/kang copies a sticker, or a picture, into your own sticker pack, which I create the first time. Telegram keeps still, animated and video stickers in separate packs, so you get one of each kind, and a new one is started when a pack is full.
"""

HELP = [
    ("/getsticker", "Reply to a sticker to get it as a PNG image. Animated and video stickers come as their original file."),
    ("/kang [emoji]", "Reply to a sticker or a picture to add it to your sticker pack. The emoji is optional: a sticker keeps its own."),
]

HELP_NOTES = [
    (
        "Good to know",
        """
        Pictures are scaled to fit a sticker, 512 pixels on the longest side.
        The pack belongs to you, not to me: you can rename or delete it with @Stickers.
        A pack holds up to 120 stickers.
        """,
    ),
]

router = Router(name="stickers")

PACK_LIMIT = 120
MAX_PACKS = 20
MAX_BYTES = 10 * 1024 * 1024
SIDE = 512
COOLDOWN = 5.0  # seconds between /kang from the same person
DEFAULT_EMOJI = "🫡"
_EMOJI_ENOUGH = 20  # the longest emoji argument worth looking at

_KINDS = {"static": ("s", "Stickers"), "animated": ("a", "Animated"), "video": ("v", "Video")}
_kanged_at: dict[int, float] = {}


def _format_of(sticker) -> str:
    if getattr(sticker, "is_animated", False):
        return "animated"
    if getattr(sticker, "is_video", False):
        return "video"
    return "static"


def _png(data: bytes, fit: bool) -> bytes:
    """A still image as a PNG. With fit, scaled so that its longest side is 512 pixels."""
    from PIL import Image  # imported here: only these commands need it

    with Image.open(io.BytesIO(data)) as image:
        image = image.convert("RGBA")
        if fit:
            width, height = image.size
            scale = SIDE / max(width, height)
            image = image.resize((max(1, round(width * scale)), max(1, round(height * scale))), Image.LANCZOS)
        out = io.BytesIO()
        image.save(out, "PNG")
        return out.getvalue()


async def _download(message: Message, file_id: str) -> bytes:
    buffer = await message.bot.download(file_id)
    return buffer.read()


def _pack_name(user_id: int, kind: str, number: int, bot_username: str) -> str:
    return f"{_KINDS[kind][0]}{user_id}_{number}_by_{bot_username}"


def _pack_title(user, kind: str, number: int) -> str:
    base = f"{user.first_name or 'My'}'s Argo {_KINDS[kind][1].lower()}"
    return f"{base[:50]} {number}"


# --- /getsticker ------------------------------------------------------------------------------

@router.message(cmd("getsticker"))
async def cmd_getsticker(message: Message) -> None:
    replied = replied_message(message)
    sticker = getattr(replied, "sticker", None)
    if sticker is None:
        await reply(message, "Reply to the sticker you want as an image.")
        return
    try:
        data = await _download(message, sticker.file_id)
        kind = _format_of(sticker)
        if kind == "static":
            name, data = f"{sticker.file_unique_id}.png", await asyncio.to_thread(_png, data, False)
        else:
            name = f"{sticker.file_unique_id}.{'tgs' if kind == 'animated' else 'webm'}"
        await message.bot.send_document(
            message.chat.id,
            BufferedInputFile(data, filename=name),
            message_thread_id=message.message_thread_id if message.is_topic_message else None,
            reply_parameters=ReplyParameters(message_id=message.message_id, allow_sending_without_reply=True),
        )
    except Exception as exc:  # a broken file or Telegram refusing it: say so instead of staying silent
        log.warning("getsticker failed: %s", exc)
        await reply(message, "I couldn't turn that sticker into a file.")


# --- /kang ------------------------------------------------------------------------------------

def _emoji_of(args: str | None, sticker) -> str:
    """The emoji for the new sticker: the first thing typed that is not plain text, else the sticker's own."""
    typed = (args or "").strip().split(None, 1)[0] if (args or "").strip() else ""
    if typed and len(typed) <= _EMOJI_ENOUGH and not re.match(r"^[\w\s]+$", typed):
        return typed
    return getattr(sticker, "emoji", None) or DEFAULT_EMOJI


async def _find_pack(bot, user_id: int, kind: str, username: str) -> tuple[str, int, bool]:
    """(name, number, exists) of the pack of this kind that has room for one more sticker."""
    for number in range(1, MAX_PACKS + 1):
        name = _pack_name(user_id, kind, number, username)
        try:
            pack = await bot.get_sticker_set(name)
        except TelegramBadRequest:
            return name, number, False
        if len(pack.stickers) < PACK_LIMIT:
            return name, number, True
    raise RuntimeError("no room")


@router.message(cmd("kang"))
async def cmd_kang(message: Message, command: CommandObject) -> None:
    user = message.from_user
    replied = replied_message(message)
    if user is None or message.sender_chat is not None:
        await reply(message, "I need to know who you are to make a pack for you. Send this as yourself, not anonymously.")
        return
    sticker = getattr(replied, "sticker", None)
    photo = getattr(replied, "photo", None)
    if sticker is None and not photo:
        await reply(message, "Reply to a sticker or a picture to add it to your pack.")
        return
    now = time.monotonic()
    if now - _kanged_at.get(user.id, -COOLDOWN) < COOLDOWN:
        await reply(message, "Slow down, one sticker at a time.")
        return
    if len(_kanged_at) > 5000:
        _kanged_at.clear()
    _kanged_at[user.id] = now

    emoji = _emoji_of(command.args, sticker)
    try:
        if sticker is not None:
            kind, source = _format_of(sticker), sticker.file_id
        else:
            kind = "static"
            data = await _download(message, photo[-1].file_id)
            source = BufferedInputFile(await asyncio.to_thread(_png, data, True), filename="sticker.png")
        username = (await message.bot.me()).username
        name, number, exists = await _find_pack(message.bot, user.id, kind, username)
        item = InputSticker(sticker=source, format=kind, emoji_list=[emoji])
        if exists:
            await message.bot.add_sticker_to_set(user_id=user.id, name=name, sticker=item)
        else:
            await message.bot.create_new_sticker_set(
                user_id=user.id, name=name, title=_pack_title(user, kind, number), stickers=[item]
            )
    except RuntimeError:
        await reply(message, "All your packs are full. Delete some stickers with @Stickers first.")
        return
    except TelegramAPIError as exc:
        _kanged_at.pop(user.id, None)
        log.warning("kang for %s failed: %s", user.id, exc)
        await reply(message, f"Telegram refused it: {esc(_reason(exc))}")
        return
    except Exception as exc:
        _kanged_at.pop(user.id, None)
        log.warning("kang for %s failed: %s", user.id, exc)
        await reply(message, "I couldn't use that picture.")
        return

    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text="Open the pack", url=f"https://t.me/addstickers/{name}")]]
    )
    text = f"Sticker {emoji} added to your pack." if exists else f"Sticker {emoji} added to your new pack."
    await reply(message, text, reply_markup=keyboard)


def _reason(exc: TelegramAPIError) -> str:
    text = str(getattr(exc, "message", None) or exc)
    return text.split(":", 1)[-1].strip() or "unknown error"
