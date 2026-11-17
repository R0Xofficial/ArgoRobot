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
from core.i18n import tr

log = logging.getLogger(__name__)

ABOUT = "notes.about"

HELP = [
    ("/notes", "notes.help.list_every_note_in_this"),
    ("/get <name>", "notes.help.get_a_saved_note_writing"),
    ("/save <name> <reply or text>", "notes.help.save_a_note_reply_to", "common.admins"),
    ("/clear <name>", "notes.help.remove_a_note", "common.admins"),
    ("/clearAll", "notes.help.delete_every_note_in_this", "common.chat_owner"),
]

HELP_NOTES = [
    (
        "common.formatting",
        "common.bold_italic_underline_strike_spoiler_code",
    ),
    (
        "common.fillings",
        "notes.note.replaced_by_facts_about_whoever",
    ),
    (
        "common.options",
        "notes.note.put_these_anywhere_in_the",
    ),
    (
        "common.buttons",
        "notes.note.text_buttonurl_example_com_a",
    ),
    (
        "common.random_content",
        "common.separate_variants_with_and_i_send",
    ),
    (
        "common.examples",
        "notes.note.save_rules_be_kind_full",
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
    return await moderation.begin_chat_command(message, role, right=RIGHT) is not None


def _split_name(args: str) -> tuple[str, str]:
    """The name and the rest of the arguments, which may span several lines."""
    parts = args.strip().split(None, 1)
    return (parts[0] if parts else ""), (parts[1].strip() if len(parts) > 1 else "")


@router.message(cmd("notes"))
async def cmd_notes(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    found = await notestore.names(message.chat.id)
    title = tr("notes.notes_in", v=esc(message.chat.title or tr("common.this_chat")))
    if not found:
        await reply(message, tr("notes.there_are_none_yet_add_one", title=title))
        return
    lines = [title, ""] + [f"• <code>{esc(name)}</code>" for name in found[:MAX_LISTED]]
    lines += ["", tr("notes.get_one_with_get_lt_name")]
    await reply(message, "\n".join(lines))


@router.message(cmd("get"))
async def cmd_get(message: Message, command: CommandObject) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    word, _ = _split_name(command.args or "")
    if not word:
        await reply(message, tr("notes.use_get_lt_name_gt_notes"))
        return
    try:
        name = notestore.check_name(word)
    except NoteNameError as exc:
        await reply(message, esc(exc))
        return
    if not await _send_note(message, name):
        await reply(message, tr("notes.there_is_no_note_called_notes", name=esc(name)))


@router.message(cmd("save"))
async def cmd_save(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role):
        return
    word, text = _split_name(sender.args_of(message, command))
    usage = tr("notes.use_save_lt_name_gt_lt")
    if not word:
        await reply(message, usage)
        return
    try:
        name = notestore.check_name(word)
    except NoteNameError as exc:
        await reply(message, esc(exc))
        return

    replied = replied_message(message)
    media = sender.media_of(message) or sender.media_of(replied)  # media sent with the command, else the replied-to message's
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, tr("notes.tell_me_what_to_save_write", usage=usage))
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
        await reply(message, tr("notes.this_chat_is_full_notes_remove", MAX_NOTES=notestore.MAX_NOTES))
    else:
        verb = tr("notes.saved") if outcome == "added" else tr("common.updated")
        await reply(message, tr("notes.the_note_get_it_with_get", verb=verb, name=esc(name)))


@router.message(cmd("clear"))
async def cmd_clear(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _admin_only(message, role):
        return
    word, _ = _split_name(command.args or "")
    if not word:
        await reply(message, tr("notes.use_clear_lt_name_gt"))
        return
    try:
        name = notestore.check_name(word)
    except NoteNameError as exc:
        await reply(message, esc(exc))
        return
    if await notestore.remove(message.chat.id, name):
        await reply(message, tr("notes.removed_the_note", name=esc(name)))
    else:
        await reply(message, tr("notes.there_is_no_note_called", name=esc(name)))


@router.message(cmd("clearall"))
async def cmd_clearall(message: Message) -> None:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    count = len(await notestore.names(message.chat.id))
    if not count:
        await reply(message, tr("notes.there_are_no_notes_in_this"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="Delete all", callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, f"Delete all <b>{count}</b> notes of this chat? This can't be undone.", reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_clearall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer(tr("common.only_the_owner_of_this_chat"), show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await notestore.clear(message.chat.id)
        await callback.answer()
        await edit(message, tr("notes.done_notes_deleted", removed=removed), reply_markup=None)
    else:
        await callback.answer()
        await edit(message, tr("notes.cancelled_the_notes_are_unchanged"), reply_markup=None)


# --- Sending a note ---------------------------------------------------------------------------

async def _send_note(message: Message, name: str) -> bool:
    """Send a note in answer to `message`. False when the chat has no such note."""
    row = await notestore.get(message.chat.id, name)
    if row is None:
        return False
    await sender.send(
        message, message.from_user, row["content"], row["media_type"], row["media_file_id"], label=tr("notes.note", name=name)
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
