"""Saved replies to keywords"""

import logging
import time
from contextlib import suppress
from typing import Any

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import (
    CallbackQuery,
    InlineKeyboardButton,
    InlineKeyboardMarkup,
    LinkPreviewOptions,
    Message,
    ReplyParameters,
)

from config import settings
from core import filterstore, markup, moderation
from core.filterstore import FilterError
from core.filters import cmd
from core.helpers import edit, reply
from core.markup import MarkupError
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.targets import replied_message
from core.text import esc, truncate

log = logging.getLogger(__name__)

# Reply to keywords with a saved message. Filters are case-insensitive. A saved message
# can be formatted, address people by name, carry buttons, and vary at random.
ABOUT = """
Reply to keywords with a saved message. Filters are case-insensitive, and a keyword matches whole words: "bot" answers "my bot" but not "robot".

Save the message by writing it after the keyword, or by replying to a message with the command: a replied-to photo, GIF, video, sticker, document, audio or voice message is sent back as it is, with its text as the caption. The commands that change filters are for chat admins with the right to change chat info.

Under the text, formatting, fillings, buttons and random variants work as described below. Command messages never trigger a filter, and a filter answers the same keyword at most once every few seconds.
"""

HELP = [
    ("/filters", "List the active filters in this chat."),
    ("/filter <keyword> <reply or text>", "Save a filter for a single keyword. Reply to a message instead of writing text to save that message, media included.", "admins"),
    ("/filter \"a long sentence\" <reply or text>", "Save a filter for a phrase. Use double quotes.", "admins"),
    ("/filter (word1, word2, \"long sentence\") <reply or text>", "Save one filter that matches any of several triggers.", "admins"),
    ("/stop <keyword>", "Remove a filter. Accepts the same keyword forms as /filter, so a list removes several at once.", "admins"),
    ("/stopAll", "Remove every filter at once. Asks for confirmation.", "chat owner"),
]

HELP_NOTES = [
    (
        "Match modifiers",
        """
        prefix:word - match messages that start with word.
        exact:word - match messages that are exactly word.
        Both also work with a phrase: exact:"good morning".
        """,
    ),
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
        Replaced by facts about whoever said the keyword:
        {first} {last} {fullname} {username} {mention} {id} {chatname} {count}
        {username} falls back to a mention for people without one. {count} is the number of members.
        """,
    ),
    (
        "Options",
        """
        Put these anywhere in the text; they are removed and change how the reply is sent:
        {preview} - show link previews.
        {protect} - forbid forwarding and saving.
        {nonotif} - send silently.
        {mediaspoiler} - hide a photo, GIF or video behind a spoiler.
        """,
    ),
    (
        "Buttons",
        """
        [Text](buttonurl://example.com) - a button under the message, on its own row.
        [Text](buttonurl://example.com:same) - a button on the row of the previous one.
        """,
    ),
    (
        "Random content",
        """
        Separate variants with %%% and I send one of them at random each time.
        /filter hi Hello! %%% Hey there! %%% Hi, {first}!
        Each variant can have its own buttons.
        """,
    ),
    (
        "Examples",
        """
        /filter bot Hello, I am a bot!
        /filter "a long sentence" Reply to a long sentence
        /filter (bad, terrible, "not good") Stay away from negative replies
        /filter prefix:How Check google on how to ...
        /filter exact:Hi Don't just send Hi
        /filter rules *Read the rules, {first}!* [Rules](buttonurl://example.com/rules)
        """,
    ),
]

router = Router(name="filters")

# Last: by the time a message gets here, the modules that act on commands and on
# unwanted messages (flood, blacklists, ...) have had their say, and answering a
# keyword never takes a message from anyone else.
PRIORITY = 50

RIGHT = "can_change_info"
CB_CLEAR = "filters:clear:"  # filters:clear:yes / filters:clear:no
COOLDOWN = 5.0  # seconds before the same filter answers again in the same chat
MAX_LISTED = 100
NO_PREVIEW = LinkPreviewOptions(is_disabled=True)
CAPTION_LIMIT = 1024
MESSAGE_LIMIT = 4096

_answered: dict[tuple[int, str], float] = {}


# --- Reading what to save -----------------------------------------------------------

def _media_of(message: Message | None) -> tuple[str, str] | None:
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


def _text_of(message: Message) -> str:
    if message.text:
        return markup.from_entities(message.text, message.entities)
    return markup.from_entities(message.caption or "", message.caption_entities)


# --- Commands -------------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """Filters are the chat's own business: admins only, never the bot's staff."""
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=RIGHT) is not None


@router.message(cmd("filters"))
async def cmd_filters(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    found = await filterstore.patterns(message.chat.id)
    title = f"<b>Filters in {esc(message.chat.title or 'this chat')}</b>"
    if not found:
        await reply(message, f"{title}\n\nThere are none yet. Add one with /filter.")
        return
    lines = [title, ""] + [f"• <code>{esc(p)}</code>" for p in found[:MAX_LISTED]]
    if len(found) > MAX_LISTED:
        lines.append(f"…and {len(found) - MAX_LISTED} more.")
    await reply(message, "\n".join(lines))


@router.message(cmd("filter"))
async def cmd_filter(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    try:
        patterns, text = filterstore.parse_triggers(command.args or "")
    except FilterError as exc:
        await reply(
            message,
            f"{esc(exc)}\nUse /filter <keyword> <reply or text>, /filter \"a phrase\" <text> or /filter (a, b) <text>.",
        )
        return

    replied = replied_message(message)
    media = _media_of(replied)
    if not text and replied is not None:
        text = _text_of(replied)
    if not text and media is None:
        await reply(message, "Tell me what to answer with: write the text after the keyword, or reply to a message.")
        return
    if len(text) > filterstore.MAX_CONTENT:
        await reply(message, f"That reply is too long: at most {filterstore.MAX_CONTENT} characters.")
        return
    try:
        markup.check(text)
        for variant in markup.variants(text):
            html = markup.parse(variant).html
            if len(html) > MESSAGE_LIMIT:
                raise MarkupError("A variant is too long once formatted.")
            if not html and media is None:
                raise MarkupError("A message needs some text (buttons alone are not enough).")
    except MarkupError as exc:
        await reply(message, esc(exc))
        return

    added, updated, full = await filterstore.save(
        message.chat.id, patterns, text, media[0] if media else None, media[1] if media else None, message.from_user.id
    )
    lines = []
    if added:
        lines.append("Saved filter for: " + ", ".join(f"<code>{esc(p)}</code>" for p in added) + ".")
    if updated:
        lines.append("Updated filter for: " + ", ".join(f"<code>{esc(p)}</code>" for p in updated) + ".")
    if full:
        lines.append(f"This chat is full ({filterstore.MAX_FILTERS} filters). Not saved: " + ", ".join(f"<code>{esc(p)}</code>" for p in full) + ".")
    await reply(message, "\n".join(lines))


@router.message(cmd("stop"))
async def cmd_stop(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    try:
        patterns, _ = filterstore.parse_triggers(command.args or "")
    except FilterError as exc:
        await reply(message, f"{esc(exc)}\nUse /stop <keyword>.")
        return
    removed = [p for p in patterns if await filterstore.remove(message.chat.id, p)]
    missing = [p for p in patterns if p not in removed]
    lines = []
    if removed:
        lines.append("Removed the filter for: " + ", ".join(f"<code>{esc(p)}</code>" for p in removed) + ".")
    if missing:
        lines.append("There is no filter for: " + ", ".join(f"<code>{esc(p)}</code>" for p in missing) + ".")
    await reply(message, "\n".join(lines))


@router.message(cmd("stopall", "stopAll"))
async def cmd_stopall(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len(await filterstore.patterns(message.chat.id))
    if not count:
        await reply(message, "There are no filters in this chat.")
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Remove all", callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text="Cancel", callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, f"Remove all <b>{count}</b> filters of this chat? This cannot be undone.", reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_stopall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer("Only the owner of this chat can do this.", show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await filterstore.clear(message.chat.id)
        await callback.answer()
        await edit(message, f"Done. <b>{removed}</b> filters removed.", reply_markup=None)
    else:
        await callback.answer()
        await edit(message, "Cancelled. The filters are unchanged.", reply_markup=None)


# --- Answering ------------------------------------------------------------------------------

def _looks_like_command(text: str) -> bool:
    return len(text) > 1 and text[0] in settings.COMMAND_PREFIXES and text[1].isalpha()


async def _matched(message: Message) -> dict[str, Any] | bool:
    user = message.from_user
    text = message.text or message.caption
    if (
        not text
        or user is None
        or user.is_bot
        or message.chat.type not in GROUP_TYPES
        or _looks_like_command(text)
    ):
        return False
    pattern = await filterstore.match(message.chat.id, text)
    if pattern is None:
        return False
    now = time.monotonic()
    key = (message.chat.id, pattern)
    if now - _answered.get(key, -COOLDOWN) < COOLDOWN:
        return False
    if len(_answered) > 5000:
        _answered.clear()
    _answered[key] = now
    return {"filter_pattern": pattern}


async def _fillings(message: Message, source: str) -> dict[str, str]:
    user = message.from_user
    values = {
        "first": user.first_name or "",
        "last": user.last_name or "",
        "fullname": user.full_name,
        "username": f"@{user.username}" if user.username else "",
        "mention": user.full_name,
        "id": str(user.id),
        "chatname": message.chat.title or "",
        "count": "",
    }
    if "{count}" in source:
        with suppress(TelegramAPIError):
            values["count"] = str(await message.bot.get_chat_member_count(message.chat.id))
    return values


def _keyboard(parsed: markup.Parsed) -> InlineKeyboardMarkup | None:
    if not parsed.buttons:
        return None
    return InlineKeyboardMarkup(
        inline_keyboard=[[InlineKeyboardButton(text=text, url=url) for text, url in row] for row in parsed.buttons]
    )


async def _send_media(message: Message, kind: str, file_id: str, caption: str | None, extra: dict[str, Any]) -> None:
    method = {
        "photo": "send_photo",
        "video": "send_video",
        "animation": "send_animation",
        "document": "send_document",
        "audio": "send_audio",
        "voice": "send_voice",
        "sticker": "send_sticker",
        "video_note": "send_video_note",
    }[kind]
    kwargs: dict[str, Any] = {
        "chat_id": message.chat.id,
        kind: file_id,
        "message_thread_id": message.message_thread_id if message.is_topic_message else None,
        "reply_parameters": ReplyParameters(message_id=message.message_id, allow_sending_without_reply=True),
        **extra,
    }
    if caption is not None and kind not in ("sticker", "video_note"):
        kwargs["caption"] = caption
    await getattr(message.bot, method)(**kwargs)


@router.message(_matched)
async def on_match(message: Message, bot: Bot, filter_pattern: str) -> None:
    row = await filterstore.get(message.chat.id, filter_pattern)
    if row is None:
        return
    source = markup.pick(row["content"])
    try:
        parsed = markup.parse(source, await _fillings(message, source))
    except MarkupError:
        log.warning("Filter %r in chat %s can no longer be sent", filter_pattern, message.chat.id)
        return

    keyboard = _keyboard(parsed)
    extra: dict[str, Any] = {}
    if "protect" in parsed.options:
        extra["protect_content"] = True
    if "nonotif" in parsed.options:
        extra["disable_notification"] = True

    kind, file_id = row["media_type"], row["media_file_id"]
    text_extra = {
        **extra,
        "link_preview_options": LinkPreviewOptions(is_disabled="preview" not in parsed.options),
    }
    if kind:
        media_extra = dict(extra)
        if "mediaspoiler" in parsed.options and kind in ("photo", "video", "animation"):
            media_extra["has_spoiler"] = True
        # A caption has to be short and a sticker has none: then the text follows on its own.
        inline = bool(parsed.html) and len(parsed.html) <= CAPTION_LIMIT and kind not in ("sticker", "video_note")
        text_follows = bool(parsed.html) and not inline
        if keyboard and not text_follows:
            media_extra["reply_markup"] = keyboard  # nothing else carries the buttons
        try:
            await _send_media(message, kind, file_id, parsed.html if inline else None, media_extra)
        except TelegramAPIError:
            log.warning("Filter %r in chat %s: its media can't be sent", filter_pattern, message.chat.id)
            if not parsed.html:
                return
            text_follows = True  # fall back to the text alone
        if not text_follows:
            return
    with suppress(TelegramAPIError):
        await reply(message, parsed.html, reply_markup=keyboard, **text_extra)
