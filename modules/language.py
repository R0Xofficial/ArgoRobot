"""Language of the bot's texts"""

from aiogram import F, Router
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, InlineKeyboardButton, InlineKeyboardMarkup, Message

from core import i18n, moderation
from core.filters import cmd
from core.helpers import edit, reply
from core.i18n import tr
from core.roles import Role
from core.text import esc

HELP_TITLE = "language.title"

ABOUT = "language.about"

HELP_WHO = "common.admins"

HELP = [
    ("/setlang [code]", "language.help.show_the_language_of_this"),
]

router = Router(name="language")

CB = "lang:"
RIGHT = "can_change_info"
COLUMNS = 2


def _keyboard(current: str) -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=("✓ " if code == current else "") + i18n.label(code), callback_data=f"{CB}{code}")
        for code in i18n.LANGUAGES
    ]
    rows = [buttons[i:i + COLUMNS] for i in range(0, len(buttons), COLUMNS)]
    return InlineKeyboardMarkup(inline_keyboard=rows)


def _status(code: str) -> str:
    listing = "\n".join(f"{esc(i18n.label(known))} (<code>{known}</code>)" for known in i18n.LANGUAGES)
    return tr("language.the_language_of_this_chat_is", language=esc(i18n.label(code)), listing=listing)


@router.message(cmd("setlang"))
async def cmd_language(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type != "private" and await moderation.begin_chat_command(message, role, right=RIGHT) is None:
        return
    chat_id = message.chat.id
    argument = (command.args or "").strip().lower()
    if argument:
        if argument not in i18n.LANGUAGES:
            await reply(message, tr("language.i_don_t_speak_that_use", codes=", ".join(f"<code>{code}</code>" for code in i18n.LANGUAGES)))
            return
        await i18n.set_chat_language(chat_id, argument)
        await i18n.use_chat(chat_id)
        await reply(message, tr("language.done_the_language_of_this_chat", language=esc(i18n.label(argument))))
        return
    current = await i18n.chat_language(chat_id)
    await reply(message, _status(current), reply_markup=_keyboard(current))


@router.callback_query(F.data.startswith(CB))
async def cb_language(callback: CallbackQuery, role: Role | None = None) -> None:
    code = (callback.data or "")[len(CB):]
    message = callback.message if isinstance(callback.message, Message) else None
    if message is None or code not in i18n.LANGUAGES:
        await callback.answer(tr("common.this_button_is_no_longer_valid"), show_alert=True)
        return
    if message.chat.type != "private" and await moderation.authorize_callback(callback, role, right=RIGHT) is None:
        return
    await i18n.set_chat_language(message.chat.id, code)
    await i18n.use_chat(message.chat.id)
    await callback.answer()
    await edit(message, _status(code), reply_markup=_keyboard(code))
