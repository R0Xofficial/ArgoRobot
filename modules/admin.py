"""Group administration"""

from typing import Awaitable, Callable

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError, TelegramBadRequest
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, Chat, InlineKeyboardButton, InlineKeyboardMarkup, LinkPreviewOptions, Message, ReplyParameters

from core import chatsettings, moderation, sender
from core.filters import cmd
from core.helpers import edit, reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.targets import replied_message
from core.text import esc, truncate, user_link, SWITCH as _ANSWERS
from core.i18n import tr, word
from core.markup import MarkupError

ABOUT = "admin.about"

HELP = [
    ("/link [request|count <number>]", "admin.help.get_the_chat_s_invite", "common.admins"),
    ("/gtitle <new title>", "admin.help.change_the_chat_s_title", "common.admins"),
    ("/adminlist", "admin.help.list_the_admins_of_this"),
    ("/pinned", "admin.help.show_the_message_pinned_in"),
    ("/pin [loud]", "admin.help.reply_to_a_message_to", "common.admins"),
    ("/permapin <text>", "admin.help.have_me_send_a_message", "common.admins"),
    ("/unpinall", "admin.help.unpin_every_pinned_message_of", "common.admins"),
    ("/unpin", "admin.help.reply_to_a_pinned_message", "common.admins"),
    ("/antichannelpin [on|off]", "admin.help.unpin_the_posts_of_the", "common.admins"),
    ("/cleanlinked [on|off]", "admin.help.delete_the_posts_of_the", "common.admins"),
    ("/cleanbluetext [on|off]", "admin.help.delete_commands_after_i_have", "common.admins"),
    ("/adminerror [on|off]", "admin.help.choose_whether_i_answer_people", "common.chat_owner"),
    ("/anonadmin [on|off]", "admin.help.let_anonymous_admins_use_every", "common.chat_owner"),
    ("/staffhelp [on|off]", "admin.help.let_my_staff_sudo_and", "common.chat_owner"),
]

router = Router(name="admin")

NO_PREVIEW = LinkPreviewOptions(is_disabled=True)
CLEAN_RIGHT = "can_change_info"  # the admin right needed for /cleanbluetext
MAX_TITLE = 128  # Telegram's limit for a chat title
MAX_LINK_USES = 99999
MAX_ADMINS_SHOWN = 80



def _state(enabled: bool) -> str:
    return tr("common.on") if enabled else tr("common.off")


# --- Switches the chat's owner controls -------------------------------------------------

async def _switch(
    message: Message,
    command: CommandObject,
    role: Role | None,
    *,
    read: Callable[[int], Awaitable[bool]],
    write: Callable[[int, bool], Awaitable[None]],
    status_text: Callable[[bool], str],
    changed_text: Callable[[bool], str],
    usage: str,
) -> None:
    """
    A yes/no setting of the chat. Any admin can look at it; only the chat's owner
    can change it. The bot's staff can't change it either.
    """
    argument = (command.args or "").strip().lower()

    if not argument:
        # can_manage_chat is held by every admin: looking is for any of them.
        if await moderation.begin_chat_command(message, role, right="can_manage_chat") is None:
            return
        await reply(message, status_text(await read(message.chat.id)))
        return

    if argument not in _ANSWERS:
        await reply(message, usage)
        return

    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return

    enabled = _ANSWERS[argument]
    await write(message.chat.id, enabled)
    await reply(message, changed_text(enabled))


@router.message(cmd("staffhelp"))
async def cmd_staffhelp(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """
    Whether the bot's staff (Owner, Developers, Sudo) may help moderate this chat.

    On by default. Only the chat's owner can change it; any admin can look.
    When it is off, the staff's commands are treated like those of any other member.
    Staff can't change this switch, whatever it says.
    """

    def changed(enabled: bool) -> str:
        if enabled:
            return (
                tr("admin.help_from_the_bot_s_staff")
            )
        return (
            tr("admin.help_from_the_bot_s_staff_2")
        )

    await _switch(
        message, command, role,
        read=chatsettings.staff_help,
        write=chatsettings.set_staff_help,
        status_text=lambda enabled: (
            tr("admin.help_from_the_bot_s_staff_3", state=_state(enabled))
        ),
        changed_text=changed,
        usage=tr("admin.use_staffhelp_on_or_staffhelp_off"),
    )


@router.message(cmd("adminerror"))
async def cmd_adminerror(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """Whether people who are not admins are told so when they try an admin command. On by default."""
    await _switch(
        message, command, role,
        read=chatsettings.admin_errors,
        write=chatsettings.set_admin_errors,
        status_text=lambda enabled: (
            tr("admin.error_replies_are_in_this_chat", state=_state(enabled))
        ),
        changed_text=lambda enabled: (
            tr("admin.error_replies_are_now_on_people")
            if enabled
            else tr("admin.error_replies_are_now_off_admin")
        ),
        usage=tr("admin.use_adminerror_on_or_adminerror_off"),
    )


@router.message(cmd("anonadmin"))
async def cmd_anonadmin(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """
    Whether anonymous admins may use every command without their rights being
    checked. Telegram hides which admin is behind an anonymous message, so their
    rights cannot be known. Off by default.
    """
    await _switch(
        message, command, role,
        read=chatsettings.anon_admin,
        write=chatsettings.set_anon_admin,
        status_text=lambda enabled: (
            tr("admin.trust_in_anonymous_admins_is_in", state=_state(enabled))
        ),
        changed_text=lambda enabled: (
            tr("admin.anonymous_admins_can_now_use_all")
            if enabled
            else tr("admin.anonymous_admins_are_no_longer_trusted")
        ),
        usage=tr("admin.use_anonadmin_on_or_anonadmin_off"),
    )


# --- /link ------------------------------------------------------------------------------------

def _parse_link_options(argument: str) -> tuple[bool, int | None]:
    """'' / 'request' / '<count>' -> (needs approval, use limit)."""
    argument = argument.strip().lower()
    if not argument:
        return False, None
    if argument == "request":
        return True, None
    words = argument.split()
    if len(words) == 2 and words[0] == "count":  # /link count 5
        words = words[1:]
    if len(words) == 1 and words[0].isdigit() and 1 <= int(words[0]) <= MAX_LINK_USES:
        return False, int(words[0])
    raise ModerationError(tr("admin.use_link_link_request_or_link", MAX_LINK_USES=MAX_LINK_USES))


@router.message(cmd("link"))
async def cmd_link(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, right="can_invite_users")
    if actor is None:
        return
    bot = message.bot
    chat = message.chat

    try:
        needs_approval, limit = _parse_link_options(command.args or "")
        me = await moderation.get_member(bot, chat.id, (await bot.me()).id)
        if me is None or not (getattr(me, "status", None) == "creator" or getattr(me, "can_invite_users", False) is True):
            raise ModerationError(tr("admin.i_need_the_admin_right_to"))
        rights = tr("admin.i_don_t_have_the_right")

        if not needs_approval and limit is None:
            # The chat's usual link: public chats have one; otherwise use the existing
            # primary link, and only make one when there is none.
            if chat.username:
                link = f"https://t.me/{chat.username}"
            else:
                full = await perform(bot.get_chat(chat.id), rights)
                link = getattr(full, "invite_link", None) or await perform(bot.export_chat_invite_link(chat.id), rights)
            await reply(message, tr("admin.invite_link", link=esc(link)), link_preview_options=NO_PREVIEW)
            return

        created = await perform(
            bot.create_chat_invite_link(
                chat.id,
                name=truncate(tr("admin.by", name=actor.name), 32),
                creates_join_request=needs_approval or None,
                member_limit=limit,
            ),
            rights,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return

    if needs_approval:
        kind = tr("admin.invite_link_that_requires_approval_to")
    else:
        kind = tr("admin.invite_link_for", limit=limit, opt=tr("admin.person") if limit == 1 else tr("admin.people"))
    await reply(
        message,
        tr("admin.was_created", kind=kind, by=moderation.by(actor), invite_link=esc(created.invite_link)),
        link_preview_options=NO_PREVIEW,
    )


# --- /gtitle -----------------------------------------------------------------------------------

@router.message(cmd("gtitle"))
async def cmd_gtitle(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, right="can_change_info")
    if actor is None:
        return
    title = (command.args or "").strip()
    try:
        if not title:
            raise ModerationError(tr("admin.give_the_new_title_for_example"))
        if len(title) > MAX_TITLE:
            raise ModerationError(tr("admin.a_chat_title_can_have_at", MAX_TITLE=MAX_TITLE))
        me = await moderation.get_member(message.bot, message.chat.id, (await message.bot.me()).id)
        if me is None or not (getattr(me, "status", None) == "creator" or getattr(me, "can_change_info", False) is True):
            raise ModerationError(tr("admin.i_need_the_admin_right_to_2"))
        await perform(
            message.bot.set_chat_title(message.chat.id, title),
            tr("admin.i_can_t_change_the_title"),
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    await reply(message, tr("admin.the_chat_title_was_changed_to", title=esc(title), by=moderation.by(actor)))


# --- /adminlist ---------------------------------------------------------------------------------

@router.message(cmd("adminlist"))
async def cmd_adminlist(message: Message) -> None:
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    try:
        admins = await message.bot.get_chat_administrators(message.chat.id)
    except TelegramAPIError:
        await reply(message, tr("admin.i_couldn_t_read_the_list"))
        return

    def line(admin) -> str:
        text = f"• {user_link(admin.user.id, admin.user.full_name)}"
        if admin.user.is_bot:
            text += tr("admin.bot")
        if getattr(admin, "is_anonymous", False):
            text += tr("admin.anonymous")
        if getattr(admin, "custom_title", None):
            text += f" — {esc(admin.custom_title)}"
        return text

    owners = [a for a in admins if getattr(a.status, "value", a.status) == "creator"]
    others = [a for a in admins if a not in owners]
    lines = [tr("admin.admins_in", title=esc(message.chat.title))]
    if owners:
        lines += ["", tr("admin.owner"), *[line(a) for a in owners]]
    if others:
        lines += ["", tr("admin.admins"), *[line(a) for a in others[:MAX_ADMINS_SHOWN]]]
        if len(others) > MAX_ADMINS_SHOWN:
            lines.append(f"…and {len(others) - MAX_ADMINS_SHOWN} more.")
    await reply(message, "\n".join(lines), link_preview_options=NO_PREVIEW)


# --- /cleanbluetext: the deleting itself is CleanCommandsMiddleware (core/middlewares.py) ---

async def _bot_can_delete(message: Message) -> bool:
    try:
        me = await message.bot.get_chat_member(message.chat.id, message.bot.id)
    except TelegramAPIError:
        return False
    return getattr(me, "status", None) == "creator" or bool(getattr(me, "can_delete_messages", False))


@router.message(cmd("cleanbluetext"))
async def cmd_cleanbluetext(message: Message, command: CommandObject, role: Role | None = None) -> None:
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if await moderation.begin_chat_command(message, role, right=CLEAN_RIGHT) is None:
        return
    usage = tr("admin.use_cleanbluetext_on_or_cleanbluetext_off")
    argument = (command.args or "").strip().lower()
    if not argument:
        current = await chatsettings.clean_commands(message.chat.id)
        await reply(message, tr("admin.commands_are", opt=tr("admin.deleted_after_they_run") if current else tr("admin.kept"), usage=usage))
        return
    if argument not in _ANSWERS:
        await reply(message, usage)
        return
    enabled = _ANSWERS[argument]
    await chatsettings.set_clean_commands(message.chat.id, enabled)
    if not enabled:
        await reply(message, tr("admin.commands_will_be_kept"))
        return
    text = tr("admin.commands_will_be_deleted_after_they")
    if not await _bot_can_delete(message):
        text += tr("admin.i_can_t_delete_messages_here")
    await reply(message, text)


# --- /pin and /unpin --------------------------------------------------------------------------------

PIN_RIGHT = "can_pin_messages"
PIN_REFUSED = "admin.pin_refused"
_LOUD = ("loud", "notify", "violent")


@router.message(cmd("pin"))
async def cmd_pin(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, right=PIN_RIGHT)
    if actor is None:
        return
    argument = (command.args or "").strip().lower()
    if argument and argument not in _LOUD:
        await reply(message, tr("admin.use_pin_as_a_reply_to"))
        return
    target = await moderation.require_reply(message)
    if target is None:
        return
    try:
        await perform(
            message.bot.pin_chat_message(message.chat.id, target.message_id, disable_notification=not argument),
            PIN_REFUSED,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        await reply(message, esc(_pin_problem(exc, "pin")))
        return
    await reply(
        message,
        tr("admin.the_message_was_pinned") + moderation.by(actor) + (tr("admin.and_everyone_was_notified") if argument else "."),
    )


@router.message(cmd("unpin"))
async def cmd_unpin(message: Message, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, right=PIN_RIGHT)
    if actor is None:
        return
    target = replied_message(message)
    try:
        await perform(
            message.bot.unpin_chat_message(message.chat.id, message_id=target.message_id if target else None),
            PIN_REFUSED,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        await reply(message, esc(_pin_problem(exc, "unpin")))
        return
    what = tr("admin.the_message") if target else tr("admin.the_most_recent_pinned_message")
    await reply(message, tr("admin.was_unpinned", what=what, by=moderation.by(actor)))


CB_UNPINALL = "unpinall:"  # unpinall:yes / unpinall:no


@router.message(cmd("unpinall"))
async def cmd_unpinall(message: Message, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role, right=PIN_RIGHT) is None:
        return
    keyboard = InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text=tr("admin.unpin_all"), callback_data=f"{CB_UNPINALL}yes"),
                InlineKeyboardButton(text=tr("common.cancel"), callback_data=f"{CB_UNPINALL}no"),
            ]
        ]
    )
    await reply(message, tr("admin.unpin_every_pinned_message_of_this"), reply_markup=keyboard)


@router.callback_query(F.data.startswith(CB_UNPINALL))
async def cb_unpinall(callback: CallbackQuery, role: Role | None = None) -> None:
    allowed = await moderation.authorize_callback(callback, role, right=PIN_RIGHT)
    if allowed is None:
        return
    message, actor = allowed
    if callback.data == f"{CB_UNPINALL}no":
        await callback.answer()
        await edit(message, tr("admin.cancelled_nothing_was_unpinned"))
        return
    try:
        await perform(callback.bot.unpin_all_chat_messages(message.chat.id), PIN_REFUSED)
    except ModerationError as exc:
        await callback.answer(str(exc), show_alert=True)
        return
    except TelegramBadRequest as exc:
        await callback.answer(_pin_problem(exc, "unpin"), show_alert=True)
        return
    await callback.answer()
    await edit(message, tr("admin.every_pinned_message_was_unpinned", by=moderation.by(actor), staff_note=moderation.staff_note(actor)))


@router.message(cmd("pinned"))
async def cmd_pinned(message: Message) -> None:
    if message.chat.type not in moderation.GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    try:
        chat = await message.bot.get_chat(message.chat.id)
    except TelegramAPIError:
        chat = None
    pinned = getattr(chat, "pinned_message", None)
    if pinned is None:
        await reply(message, tr("admin.there_is_no_pinned_message"))
        return
    markup = None
    if message.chat.type == "supergroup":
        if message.chat.username:
            url = f"https://t.me/{message.chat.username}/{pinned.message_id}"
        else:
            url = f"https://t.me/c/{str(message.chat.id).removeprefix('-100')}/{pinned.message_id}"
        markup = InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text=tr("admin.button.go_to_the_pinned_message"), url=url)]])
    await reply(
        message,
        tr("admin.the_pinned_message_is_here"),
        quote=False,
        reply_parameters=ReplyParameters(message_id=pinned.message_id, allow_sending_without_reply=True),
        reply_markup=markup,
    )


@router.message(cmd("permapin"))
async def cmd_permapin(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor = await moderation.begin_chat_command(message, role, right=PIN_RIGHT)
    if actor is None:
        return
    replied = replied_message(message)
    text = sender.args_of(message, command)
    media = sender.media_of(message) or sender.media_of(replied)
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, tr("admin.use_permapin_lt_text_gt_or"))
        return
    try:
        sender.validate(text, media is not None)
    except MarkupError as exc:
        await reply(message, esc(str(exc)))
        return
    # Sent as a message of its own, not as an answer to the command.
    base = Message(
        message_id=0,
        date=message.date,
        chat=Chat(id=message.chat.id, type=message.chat.type, title=message.chat.title),
        message_thread_id=message.message_thread_id if message.is_topic_message else None,
        is_topic_message=message.is_topic_message if message.is_topic_message else None,
    ).as_(message.bot)
    try:
        sent = await perform(
            sender.send(
                base,
                message.from_user,
                text,
                media[0] if media else None,
                media[1] if media else None,
                label=tr("admin.permanent_pin"),
            ),
            PIN_REFUSED,
        )
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    if sent is None:
        await reply(message, tr("admin.i_couldn_t_send_that_message"))
        return
    try:
        await perform(message.bot.pin_chat_message(message.chat.id, sent.message_id, disable_notification=True), PIN_REFUSED)
    except ModerationError as exc:
        await moderation.deny(message, exc)
    except TelegramBadRequest as exc:
        await reply(message, esc(_pin_problem(exc, "pin")))


async def _linked_switch(message: Message, command: CommandObject, role: Role | None, *, usage: str, get, put, on: str, off: str) -> None:
    if message.chat.type != "supergroup":
        await reply(message, tr("admin.this_only_works_in_a_supergroup"))
        return
    if await moderation.begin_chat_command(message, role, right=PIN_RIGHT) is None:
        return
    argument = (command.args or "").strip().lower()
    if not argument:
        current = await get(message.chat.id)
        await reply(message, tr("admin.current_setting", opt=tr("common.on") if current else tr("common.off"), usage=usage))
        return
    if argument not in _ANSWERS:
        await reply(message, usage)
        return
    enabled = _ANSWERS[argument]
    await put(message.chat.id, enabled)
    await reply(message, on if enabled else off)


@router.message(cmd("antichannelpin"))
async def cmd_antichannelpin(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _linked_switch(
        message, command, role,
        usage=tr("admin.use_antichannelpin_on_or_antichannelpin_off"),
        get=chatsettings.anti_channel_pin, put=chatsettings.set_anti_channel_pin,
        on=tr("admin.posts_of_the_linked_channel_will"), off=tr("admin.posts_of_the_linked_channel_stay"),
    )


@router.message(cmd("cleanlinked"))
async def cmd_cleanlinked(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _linked_switch(
        message, command, role,
        usage=tr("admin.use_cleanlinked_on_or_cleanlinked_off"),
        get=chatsettings.clean_linked, put=chatsettings.set_clean_linked,
        on=tr("admin.posts_of_the_linked_channel_will_be_deleted"), off=tr("admin.posts_of_the_linked_channel_are_kept"),
    )


def _pin_problem(exc: Exception, action: str) -> str:
    """A sentence for the failures Telegram reports as plain bad requests."""
    problem = str(exc).lower()
    if "not found" in problem or "message_id_invalid" in problem:
        return tr("admin.that_message_isn_t_pinned") if action == "unpin" else tr("admin.i_can_t_find_that_message")
    if "chat_not_modified" in problem or "not modified" in problem:
        return tr("admin.that_message_is_already_pinned")
    return tr("admin.i_couldn_t_that_message", action=word(action))
