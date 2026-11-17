"""Saved replies to keywords"""

import logging
import time
from typing import Any

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, LinkPreviewOptions, Message

from config import settings
from core import filterstore, moderation, sender
from core.filterstore import FilterError
from core.filters import cmd
from core.helpers import edit, reply
from core.markup import MarkupError
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.targets import replied_message
from core.text import esc, truncate
from core.i18n import tr

log = logging.getLogger(__name__)

# Reply to keywords with a saved message. Filters are case-insensitive. A saved message
# can be formatted, address people by name, carry buttons, and vary at random.
ABOUT = "filters.about"

HELP = [
    ("/filters", "filters.help.list_the_active_filters_in"),
    ("/filter <keyword> <reply or text>", "filters.help.save_a_filter_for_a", "common.admins"),
    ("/filter \"a long sentence\" <reply or text>", "filters.help.save_a_filter_for_a_2", "common.admins"),
    ("/filter (word1, word2, \"long sentence\") <reply or text>", "filters.help.save_one_filter_that_matches", "common.admins"),
    ("/stop <keyword>", "filters.help.remove_a_filter_accepts_the", "common.admins"),
    ("/stopAll", "filters.help.remove_every_filter_at_once", "common.chat_owner"),
]

HELP_NOTES = [
    (
        "common.match_modifiers",
        "filters.note.prefix_word_match_messages_that",
    ),
    (
        "common.formatting",
        "common.bold_italic_underline_strike_spoiler_code",
    ),
    (
        "common.fillings",
        "filters.note.replaced_by_facts_about_whoever",
    ),
    (
        "common.options",
        "filters.note.put_these_anywhere_in_the",
    ),
    (
        "common.buttons",
        "common.text_buttonurl_example_com_a_button",
    ),
    (
        "common.random_content",
        "filters.note.separate_variants_with_and_i",
    ),
    (
        "common.examples",
        "filters.note.filter_bot_hello_i_am",
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

_answered: dict[tuple[int, str], float] = {}


# --- Reading what to save -----------------------------------------------------------

# --- Commands -------------------------------------------------------------------------------

async def _admin_only(message: Message, role: Role | None) -> bool:
    """Filters are the chat's own business: admins only, never the bot's staff."""
    return await moderation.begin_chat_command(message, role, right=RIGHT) is not None


@router.message(cmd("filters"))
async def cmd_filters(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    found = await filterstore.patterns(message.chat.id)
    title = tr("filters.filters_in", v=esc(message.chat.title or tr("common.this_chat")))
    if not found:
        await reply(message, tr("filters.there_are_none_yet_add_one", title=title))
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
        patterns, text = filterstore.parse_triggers(sender.args_of(message, command))
    except FilterError as exc:
        await reply(
            message,
            tr("filters.use_filter_lt_keyword_gt_lt", exc=esc(exc)),
        )
        return

    replied = replied_message(message)
    media = sender.media_of(message) or sender.media_of(replied)  # media sent with the command, else the replied-to message's
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, tr("filters.tell_me_what_to_answer_with"))
        return
    try:
        sender.validate(text, media is not None)
    except MarkupError as exc:
        await reply(message, esc(exc))
        return

    added, updated, full = await filterstore.save(
        message.chat.id, patterns, text, media[0] if media else None, media[1] if media else None, message.from_user.id
    )
    lines = []
    if added:
        lines.append(tr("filters.saved_filter_for") + ", ".join(f"<code>{esc(p)}</code>" for p in added) + ".")
    if updated:
        lines.append(tr("filters.updated_filter_for") + ", ".join(f"<code>{esc(p)}</code>" for p in updated) + ".")
    if full:
        lines.append(tr("filters.this_chat_is_full_filters_not", MAX_FILTERS=filterstore.MAX_FILTERS) + ", ".join(f"<code>{esc(p)}</code>" for p in full) + ".")
    await reply(message, "\n".join(lines))


@router.message(cmd("stop"))
async def cmd_stop(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if not await _admin_only(message, role):
        return
    try:
        patterns, _ = filterstore.parse_triggers(command.args or "")
    except FilterError as exc:
        await reply(message, tr("filters.use_stop_lt_keyword_gt", exc=esc(exc)))
        return
    removed = [p for p in patterns if await filterstore.remove(message.chat.id, p)]
    missing = [p for p in patterns if p not in removed]
    lines = []
    if removed:
        lines.append(tr("filters.removed_the_filter_for") + ", ".join(f"<code>{esc(p)}</code>" for p in removed) + ".")
    if missing:
        lines.append(tr("filters.there_is_no_filter_for") + ", ".join(f"<code>{esc(p)}</code>" for p in missing) + ".")
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
        await reply(message, tr("filters.there_are_no_filters_in_this"))
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text=tr("common.remove_all"), callback_data=f"{CB_CLEAR}yes"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_CLEAR}no"),
            ]
        ]
    )
    await reply(message, tr("filters.remove_all_filters_of_this_chat", count=count), reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_CLEAR))
async def cb_stopall(callback: CallbackQuery) -> None:
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    if await moderation.member_status(callback.bot, message.chat.id, callback.from_user.id) != "creator":
        await callback.answer(tr("common.only_the_owner_of_this_chat"), show_alert=True)
        return
    if callback.data == f"{CB_CLEAR}yes":
        removed = await filterstore.clear(message.chat.id)
        await callback.answer()
        await edit(message, tr("filters.done_filters_removed", removed=removed), reply_markup=None)
    else:
        await callback.answer()
        await edit(message, tr("filters.cancelled_the_filters_are_unchanged"), reply_markup=None)


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


@router.message(_matched)
async def on_match(message: Message, bot: Bot, filter_pattern: str) -> None:
    row = await filterstore.get(message.chat.id, filter_pattern)
    if row is None:
        return
    await sender.send(
        message, message.from_user, row["content"], row["media_type"], row["media_file_id"], label=tr("filters.filter", filter_pattern=filter_pattern)
    )
