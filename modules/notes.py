"""Saved messages anyone can ask for"""

import logging
import time
from typing import Any

from aiogram import F, Router
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import moderation, notestore, sender
from core.filters import cmd
from core.helpers import edit, reply
from core.markup import MarkupError
from core.moderation import GROUP_TYPES, ModerationError
from core.notestore import NoteNameError
from core.roles import Role
from core.targets import replied_message
from core.text import esc

log = logging.getLogger(__name__)

ABOUT = """
Save a message under a name, and anyone in the chat can ask for it later with /get name or by writing #name. Good for rules, links and answers to questions that keep coming back.

A note can be text, or any media: a photo, video, GIF, sticker, document, audio or voice message, with text as its caption. Write the text after the name, or reply to a message to save that message as it is. The commands that change notes are for chat admins with the right to change chat info.

Names are one word, and case does not matter. Under the text, formatting, fillings, buttons and random variants work as described below.
"""

HELP = [
    ("/notes", "List every note in this chat."),
    ("/get <name>", "Get a saved note. Writing #name does the same."),
    ("/save <name> <reply or text>", "Save a note. Reply to a message instead of writing text to save that message, media included. Saving under an existing name replaces the note.", "admins"),
    ("/clear <name>", "Remove a note.", "admins"),
    ("/clearAll", "Delete every note in this chat. Asks for confirmation.", "chat owner"),
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
        Replaced by facts about whoever asks for the note:
        {first} {last} {fullname} {username} {mention} {id} {chatname} {count}
        {username} falls back to a mention for people without one. {count} is the number of members.
        """,
    ),
    (
        "Options",
        """
        Put these anywhere in the text; they are removed and change how the note is sent:
        {preview} - show link previews.
        {protect} - forbid forwarding and saving.
        {nonotif} - send silently.
        {mediaspoiler} - hide a photo, GIF or video behind a spoiler.
        """,
    ),
    (
        "Buttons",
        """
        [Text](buttonurl://example.com) - a button under the note, on its own row.
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
        /save rules *Be kind, {first}.* [Full rules](buttonurl://example.com/rules)
        /save logo - reply to a photo to save the photo.
        /save faq - reply to a document to save the file.
        /get rules
        #rules
        """,
    ),
]

router = Router(name="notes")

# After the modules that act on messages (flood, blacklists...), before keyword filters.
PRIORITY = 40

RIGHT = "can_change_info"
CB_CLEAR = "notes:clear:"  # notes:clear:yes / notes:clear:no
COOLDOWN = 3.0  # seconds before #name answers again in the same chat
MAX_LISTED = 300

_answered: dict[tuple[int, str], float] = {}


# --- Commands -------------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """Notes are the chat's own business: admins only, never the bot's staff."""
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=RIGHT) is not None


def _split_name(args: str) -> tuple[str, str]:
    """The name and the rest of the arguments, which may span several lines."""
    parts = args.strip().split(None, 1)
    return (parts[0] if parts else ""), (parts[1].strip() if len(parts) > 1 else "")


@router.message(cmd("notes"))
async def cmd_notes(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    found = await notestore.names(message.chat.id)
    title = f"<b>Notes in {esc(message.chat.title or 'this chat')}</b>"
    if not found:
        await reply(message, f"{title}\n\nThere are none yet. Add one with /save.")
        return
    lines = [title, ""] + [f"• <code>{esc(name)}</code>" for name in found[:MAX_LISTED]]
    lines += ["", "Get one with /get &lt;name&gt; or #name."]
    await reply(message, "\n".join(lines))


@router.message(cmd("get"))
async def cmd_get(message: Message, command: CommandObject) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    word, _ = _split_name(command.args or "")
    if not word:
        await reply(message, "Use /get &lt;name&gt;. /notes lists them.")
        return
    try:
        name = notestore.check_name(word)
    except NoteNameError as exc:
        await reply(message, esc(exc))
        return
    if not await _send_note(message, name):
        await reply(message, f"There is no note called <code>{esc(name)}</code>. /notes lists them.")


@router.message(cmd("save"))
async def cmd_save(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role):
        return
    word, text = _split_name(command.args or "")
    usage = "Use /save &lt;name&gt; &lt;text&gt;, or reply to a message with /save &lt;name&gt;."
    if not word:
        await reply(message, usage)
        return
    try:
        name = notestore.check_name(word)
    except NoteNameError as exc:
        await reply(message, esc(exc))
        return

    replied = replied_message(message)
    media = sender.media_of(replied)
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, f"Tell me what to save: write the text after the name, or reply to a message.\n{usage}")
        return
    try:
        sender.validate(text, media is not None)
    except MarkupError as exc:
        await reply(message, esc(exc))
        return

    outcome = await notestore.save(
        message.chat.id, name, text, media[0] if media else None, media[1] if media else None, message.from_user.id
    )
    if outcome == "full":
        await reply(message, f"This chat is full ({notestore.MAX_NOTES} notes). Remove one with /clear first.")
    else:
        verb = "Saved" if outcome == "added" else "Updated"
        await reply(message, f"{verb} the note <code>{esc(name)}</code>. Get it with /get {esc(name)} or #{esc(name)}.")


@router.message(cmd("clear"))
async def cmd_clear(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _admin_only(message, role):
        return
    word, _ = _split_name(command.args or "")
    if not word:
        await reply(message, "Use /clear &lt;name&gt;.")
        return
    try:
        name = notestore.check_name(word)
    except NoteNameError as exc:
        await reply(message, esc(exc))
        return
    if await notestore.remove(message.chat.id, name):
        await reply(message, f"Removed the note <code>{esc(name)}</code>.")
    else:
        await reply(message, f"There is no note called <code>{esc(name)}</code>.")


@router.message(cmd("clearall"))
async def cmd_clearall(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len(await notestore.names(message.chat.id))
    if not count:
        await reply(message, "There are no notes in this chat.")
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Delete all", callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text="Cancel", callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, f"Delete all <b>{count}</b> notes of this chat? This cannot be undone.", reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_clearall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer("This button is no longer valid.", show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer("Only the owner of this chat can do this.", show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await notestore.clear(message.chat.id)
        await callback.answer()
        await edit(message, f"Done. <b>{removed}</b> notes deleted.", reply_markup=None)
    else:
        await callback.answer()
        await edit(message, "Cancelled. The notes are unchanged.", reply_markup=None)


# --- Sending a note ---------------------------------------------------------------------------

async def _send_note(message: Message, name: str) -> bool:
    """Send a note in answer to `message`. False when the chat has no such note."""
    row = await notestore.get(message.chat.id, name)
    if row is None:
        return False
    await sender.send(
        message, message.from_user, row["content"], row["media_type"], row["media_file_id"], label=f"note {name!r}"
    )
    return True


async def _hashtag(message: Message) -> dict[str, Any] | bool:
    """A message that starts with #name, where name is a note of this chat."""
    text = message.text or message.caption
    user = message.from_user
    if not text or user is None or user.is_bot or message.chat.type not in GROUP_TYPES or not text.startswith("#"):
        return False
    try:
        name = notestore.check_name(text.split(None, 1)[0])
    except NoteNameError:
        return False
    if await notestore.get(message.chat.id, name) is None:
        return False
    now = time.monotonic()
    key = (message.chat.id, name)
    if now - _answered.get(key, -COOLDOWN) < COOLDOWN:
        return False
    if len(_answered) > 5000:
        _answered.clear()
    _answered[key] = now
    return {"note_name": name}


@router.message(_hashtag)
async def on_hashtag(message: Message, note_name: str) -> None:
    await _send_note(message, note_name)
