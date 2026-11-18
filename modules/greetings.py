"""Welcome and goodbye messages"""

import logging
from contextlib import suppress
from datetime import datetime, timedelta, timezone

from aiogram import Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Chat, ChatMemberUpdated, ChatPermissions, Message

from core import antiraid, approvals, captchas, chatlog, disabled, banguard, greetings, immunity, memberevents, moderation, registry, roles, sender, staffgreet
from core.filters import cmd
from core.help_index import Section
from core.helpers import reply
from core.markup import MarkupError
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.targets import replied_message
from core.text import esc, user_ref, SWITCH as _ANSWERS
from core.i18n import tr

log = logging.getLogger(__name__)

ABOUT = "greetings.about"

S_WELCOME, S_GOODBYE = Section("greetings.sections"), Section("greetings.sections_2")

HELP = [
    ("/welcome", "greetings.help.show_whether_welcomes_are_on", "common.admins", S_WELCOME),
    ("/welcome noformat", "greetings.help.show_the_welcome_message_exactly", "common.admins", S_WELCOME),
    ("/welcome <on|off>", "greetings.help.turn_welcomes_on_or_off", "common.admins", S_WELCOME),
    ("/setwelcome <reply or text>", "greetings.help.set_a_custom_welcome_message", "common.admins", S_WELCOME),
    ("/resetwelcome", "greetings.help.go_back_to_the_default", "common.admins", S_WELCOME),
    ("/goodbye", "greetings.help.show_whether_goodbyes_are_on", "common.admins", S_GOODBYE),
    ("/goodbye noformat", "greetings.help.show_the_goodbye_message_exactly", "common.admins", S_GOODBYE),
    ("/goodbye <on|off>", "greetings.help.turn_goodbyes_on_or_off", "common.admins", S_GOODBYE),
    ("/setgoodbye <reply or text>", "greetings.help.set_a_custom_goodbye_message", "common.admins", S_GOODBYE),
    ("/resetgoodbye", "greetings.help.go_back_to_the_default_2", "common.admins", S_GOODBYE),
    ("/welcomemute <on|off>", "greetings.help.stop_new_members_from_sending", "common.admins", S_WELCOME),
    ("/cleanwelcome <on|off>", "greetings.help.delete_my_previous_welcome_message", "common.admins", S_WELCOME),
    ("/cleanservice <on|off>", "greetings.help.delete_the_joined_and_left", "common.admins", S_WELCOME),
]

HELP_NOTES = [
    (
        "common.formatting",
        "common.bold_italic_underline_strike_spoiler_code",
    ),
    (
        "common.fillings",
        "greetings.note.replaced_by_facts_about_the",
    ),
    (
        "common.options",
        "greetings.note.put_these_anywhere_in_the",
    ),
    (
        "common.buttons",
        "common.text_buttonurl_example_com_a_button",
    ),
    (
        "common.random_content",
        "common.separate_variants_with_and_i_send",
    ),
    (
        "common.examples",
        "greetings.note.setwelcome_hello_welcome_to_rules",
    ),
]

router = Router(name="greetings")

RIGHT = "can_change_info"  # the admin right needed to change these settings
LOOK_RIGHT = "can_manage_chat"  # every admin holds it: enough to look
MAX_GREETED = 5  # people greeted for one "joined" message (someone can add many at once)
captchas.WELCOME_LIMIT = MAX_GREETED  # the captcha button rides on these welcomes
MUTE_HOURS = 24


# Everything except plain text.
_NO_MEDIA = ChatPermissions(
    can_send_messages=True,
    can_send_audios=False,
    can_send_documents=False,
    can_send_photos=False,
    can_send_videos=False,
    can_send_video_notes=False,
    can_send_voice_notes=False,
    can_send_polls=False,
    can_send_other_messages=False,
)


def _yes_no(value: bool) -> str:
    return tr("common.yes") if value else tr("common.no")


async def _begin(message: Message, role: Role | None, *, change: bool) -> bool:
    """The chat's own business: admins only, never the bot's staff."""
    right = RIGHT if change else LOOK_RIGHT
    return await moderation.begin_chat_command(message, role, right=right) is not None


# --- Welcome and goodbye: the same commands for both -----------------------------------------

async def _show(message: Message, role: Role | None, kind: str, argument: str) -> None:
    if not await _begin(message, role, change=False):
        return
    settings = await greetings.get(message.chat.id)
    content, media_type, file_id = greetings.message_of(settings, kind)
    custom = settings[f"{kind}_content"] is not None or settings[f"{kind}_media_type"] is not None
    head = [
        f"<b>{kind.capitalize()}</b>",
        tr("greetings.enabled", yes_no=_yes_no(settings[f'{kind}_on'])),
        tr("greetings.message", opt=tr("greetings.custom") if custom else tr("greetings.default")),
    ]
    if kind == "welcome":
        head += [
            tr("greetings.welcome_mute", yes_no=_yes_no(settings['welcome_mute'])),
            tr("greetings.clean_welcome", yes_no=_yes_no(settings['clean_welcome'])),
            tr("greetings.clean_service", yes_no=_yes_no(settings['clean_service'])),
        ]
    await reply(message, "\n".join(head))
    await sender.send(message, message.from_user, content, media_type, file_id, raw=argument == "noformat", label=kind)


async def _command(message: Message, command: CommandObject, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    argument = (command.args or "").strip().lower()
    if not argument or argument == "noformat":
        await _show(message, role, kind, argument)
        return
    if argument not in _ANSWERS:
        await reply(message, tr("greetings.use_noformat_or_on_off", kind=kind))
        return
    if not await _begin(message, role, change=True):
        return
    enabled = _ANSWERS[argument]
    plural = tr("greetings.welcomes") if kind == "welcome" else tr("greetings.goodbyes")
    if bool((await greetings.get(message.chat.id))[f"{kind}_on"]) == enabled:
        await reply(message, tr("greetings.are_already", plural=plural, opt=tr("common.on") if enabled else tr("common.off")))
        return
    await greetings.update(message.chat.id, **{f"{kind}_on": enabled})
    await reply(message, tr("greetings.are_now", plural=plural, opt=tr("common.on") if enabled else tr("common.off")))


async def _set(message: Message, command: CommandObject, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _begin(message, role, change=True):
        return
    # Typed text and a replied-to message are both turned into the HTML of core/markup.py.
    text = sender.args_of(message, command)
    replied = replied_message(message)
    media = sender.media_of(message) or sender.media_of(replied)  # media sent with the command, else the replied-to message's
    if not text and replied is not None:
        text = sender.text_of(replied)
    if not text and media is None:
        await reply(message, tr("greetings.tell_me_what_to_say_write", kind=kind))
        return
    try:
        sender.validate(text, media is not None)
    except MarkupError as exc:
        await reply(message, esc(exc))
        return
    await greetings.update(
        message.chat.id,
        **{
            f"{kind}_content": text,
            f"{kind}_media_type": media[0] if media else None,
            f"{kind}_media_file_id": media[1] if media else None,
        },
    )
    await reply(message, tr("greetings.the_message_is_saved_use_to", kind=kind))


async def _reset(message: Message, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    if not await _begin(message, role, change=True):
        return
    await greetings.update(
        message.chat.id,
        **{f"{kind}_content": None, f"{kind}_media_type": None, f"{kind}_media_file_id": None},
    )
    await reply(message, tr("greetings.the_message_is_back_to_the", kind=kind))


@router.message(cmd("welcome"))
async def cmd_welcome(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _command(message, command, role, "welcome")


@router.message(cmd("goodbye"))
async def cmd_goodbye(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _command(message, command, role, "goodbye")


@router.message(cmd("setwelcome"))
async def cmd_setwelcome(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _set(message, command, role, "welcome")


@router.message(cmd("setgoodbye"))
async def cmd_setgoodbye(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _set(message, command, role, "goodbye")


@router.message(cmd("resetwelcome"))
async def cmd_resetwelcome(message: Message, role: Role | None = None) -> None:
    await _reset(message, role, "welcome")


@router.message(cmd("resetgoodbye"))
async def cmd_resetgoodbye(message: Message, role: Role | None = None) -> None:
    await _reset(message, role, "goodbye")


# --- Switches ---------------------------------------------------------------------------------

async def _switch(
    message: Message,
    command: CommandObject,
    role: Role | None,
    column: str,
    usage: str,
    text,
) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    argument = (command.args or "").strip().lower()
    if not argument:
        if not await _begin(message, role, change=False):
            return
        current = (await greetings.get(message.chat.id))[column]
        await reply(message, f"{text(current)}\n{usage}")
        return
    if argument not in _ANSWERS:
        await reply(message, usage)
        return
    if not await _begin(message, role, change=True):
        return
    enabled = _ANSWERS[argument]
    if bool((await greetings.get(message.chat.id))[column]) == enabled:
        await reply(message, tr("greetings.this_is_already", opt=tr("common.on") if enabled else tr("common.off")))
        return
    await greetings.update(message.chat.id, **{column: enabled})
    await reply(message, text(enabled))


@router.message(cmd("welcomemute"))
async def cmd_welcomemute(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch(
        message, command, role, "welcome_mute", tr("greetings.use_welcomemute_on_or_welcomemute_off"),
        lambda on: tr("greetings.new_members_can_t_send_media", MUTE_HOURS=MUTE_HOURS)
        if on
        else tr("greetings.new_members_can_send_anything_right"),
    )


@router.message(cmd("cleanwelcome"))
async def cmd_cleanwelcome(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch(
        message, command, role, "clean_welcome", tr("greetings.use_cleanwelcome_on_or_cleanwelcome_off"),
        lambda on: tr("greetings.my_previous_welcome_message_will_be")
        if on
        else tr("greetings.old_welcome_messages_will_be_kept"),
    )


@router.message(cmd("cleanservice"))
async def cmd_cleanservice(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch(
        message, command, role, "clean_service", tr("greetings.use_cleanservice_on_or_cleanservice_off"),
        lambda on: tr("greetings.the_joined_and_left_service_messages")
        if on
        else tr("greetings.service_messages_will_be_kept"),
    )


# --- Joins and leaves ---------------------------------------------------------------------------

async def _restrict_newcomer(message: Message, user) -> None:
    """The welcome mute: no media for a day. Whoever is restricted already is left alone."""
    chat_id = message.chat.id
    if await approvals.is_approved(chat_id, user.id) or immunity.is_immune(user.id):
        return
    bot = message.bot
    try:
        member = await bot.get_chat_member(chat_id, user.id)
        if getattr(member, "status", None) != "member":
            # Muted, an admin or gone: restricting again could undo what someone decided.
            return
        until = datetime.now(timezone.utc) + timedelta(hours=MUTE_HOURS)
        await bot.restrict_chat_member(
            chat_id, user.id, permissions=_NO_MEDIA, until_date=until, use_independent_chat_permissions=True
        )
    except TelegramAPIError as exc:
        log.info("Welcome mute of %s in chat %s failed: %s", user.id, chat_id, exc)


async def _delete(message: Message) -> None:
    with suppress(TelegramAPIError):
        await message.delete()


async def _greet_staff(message: Message, user, role: Role) -> None:
    """Somebody with a role is welcomed with the message of their role, whatever the chat's settings are."""
    await sender.send(message, user, staffgreet.get(role, "welcome"), None, None, label=tr("greetings.staff_welcome_message"))


async def _greet(message: Message, people: list, settings: dict) -> None:
    """The welcome mute and the welcome message for people who are in."""
    members = []
    for user in people:
        role = await roles.get_role(user.id)
        if role is None:
            members.append(user)
        else:
            await _greet_staff(message, user, role)  # the chat's own welcome, mute and captcha are for everyone else
    people = members

    if settings["welcome_mute"]:
        for user in people:
            await _restrict_newcomer(message, user)

    if settings["welcome_on"] and people:
        content, media_type, file_id = greetings.message_of(settings, "welcome")
        previous = settings["last_welcome_id"]
        if settings["clean_welcome"] and previous:
            with suppress(TelegramAPIError):
                await message.bot.delete_message(message.chat.id, previous)
        last_sent = None
        for user in people:
            # Somebody with a captcha message of their own is welcomed once through; someone whose
            # welcome carries the captcha button gets it.
            skip, button = await captchas.welcome_for(message.bot, message.chat.id, user.id)
            if skip:
                continue
            sent = await sender.send(message, user, content, media_type, file_id, label=tr("greetings.welcome_message"), extra_button=button)
            if sent is not None:
                last_sent = sent.message_id
                if button is not None:
                    await captchas.mark_welcomed(message.chat.id, user.id)
        if last_sent is not None and settings["clean_welcome"]:
            await greetings.update(message.chat.id, last_welcome_id=last_sent)


async def _has_log(chat_id: int) -> bool:
    try:
        return await chatlog.get(chat_id) is not None
    except Exception:
        return False


async def _log_member(bot: Bot, chat, tag: str, user) -> None:
    """Who joined (#JOINED) or left on their own (#LEFT), in the chat's log (/setlog). Never raises."""
    try:
        await chatlog.record(bot, chat, tag, None, user_ref(user.id, user.full_name))
    except Exception:
        log.exception("Could not log %s", tag)


@router.message(F.new_chat_members)
async def on_join(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        return
    settings = await greetings.get(message.chat.id)
    bot_id = message.bot.id
    for user in message.new_chat_members:
        memberevents.mark("join", message.chat.id, user.id)
        if user.id != bot_id:
            await _log_member(message.bot, message.chat, "JOINED", user)
    people = []
    for user in message.new_chat_members:
        if user.is_bot or user.id == bot_id or await banguard.applies(message.bot, message.chat.id, user.id) or antiraid.was_removed(message.chat.id, user.id):
            continue  # bots are not greeted, and a globally banned user (in a chat that removes them) or one a raid took away is about to be removed
        people.append(user)
    people = people[:MAX_GREETED]

    try:
        await _greet(message, people, settings)
    finally:
        # Whatever went wrong while greeting, the "joined" message still goes when the chat asked for it.
        if settings["clean_service"]:
            await _delete(message)


async def _greet_after_captcha(bot, chat_id: int, user, join_message_id: int | None) -> None:
    """Somebody got through a captcha: this is the welcome they would have had on joining."""
    info = registry.get("greetings")
    if info is None or not info.enabled or await disabled.module_off(chat_id, "greetings"):
        return
    try:
        chat = await bot.get_chat(chat_id)
    except TelegramAPIError:
        return
    message = Message(
        message_id=join_message_id or 0,
        date=datetime.now(timezone.utc),
        chat=Chat(id=chat.id, type=chat.type, title=chat.title),
    ).as_(bot)
    await _greet(message, [user], await greetings.get(chat_id))


captchas.on_released(_greet_after_captcha)


@router.message(F.left_chat_member)
async def on_leave(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        return
    settings = await greetings.get(message.chat.id)
    left = message.left_chat_member
    memberevents.mark("leave", message.chat.id, left.id)
    # The sender of the service message is the person who left, or whoever removed them.
    left_on_own = message.from_user is not None and message.from_user.id == left.id
    if left_on_own and left.id != message.bot.id:  # a removal is in the log already, with the admin who did it
        await _log_member(message.bot, message.chat, "LEFT", left)
    try:
        await _say_goodbye(message, left, settings, left_on_own)
    finally:
        if settings["clean_service"]:
            await _delete(message)


async def _say_goodbye(message: Message, left, settings: dict, left_on_own: bool) -> None:
    """The goodbye of someone who left on their own: their role's, or the chat's when it has them on."""
    if not left_on_own or left.is_bot or left.id == message.bot.id:
        return
    role = await roles.get_role(left.id)
    if role is not None:  # staff are always seen off, with the goodbye of their role
        await sender.send(message, left, staffgreet.get(role, "goodbye"), None, None, label=tr("greetings.staff_goodbye_message"))
    elif settings["goodbye_on"]:
        content, media_type, file_id = greetings.message_of(settings, "goodbye")
        await sender.send(message, left, content, media_type, file_id, label=tr("greetings.goodbye_message"))


# --- Joins and leaves that come without a service message ---------------------------------------------
# In a group with hidden members (and in big ones) Telegram sends no "joined" or "left" message, only the
# chat_member update to admins. The update does the greeting when no service message showed up for it.

def _present(member) -> bool:
    status = str(getattr(member.status, "value", member.status))
    return status in ("member", "administrator", "creator") or (status == "restricted" and getattr(member, "is_member", True))


def _status_of(member) -> str:
    return str(getattr(member.status, "value", member.status))


def _joined(event: ChatMemberUpdated) -> bool:
    return (
        event.chat.type in GROUP_TYPES
        and not _present(event.old_chat_member)
        and _present(event.new_chat_member)
        and _status_of(event.new_chat_member) != "creator"
    )


def _left(event: ChatMemberUpdated) -> bool:
    return (
        event.chat.type in GROUP_TYPES
        and _present(event.old_chat_member)
        and _status_of(event.new_chat_member) == "left"  # not "kicked": a ban is no goodbye
    )


def _made_up_message(bot, chat) -> Message:
    """A message to answer to when there is none: the welcome goes to the chat's main topic."""
    return Message(
        message_id=0,
        date=datetime.now(timezone.utc),
        chat=Chat(id=chat.id, type=chat.type, title=chat.title),
    ).as_(bot)


async def _greetings_here(chat_id: int) -> bool:
    info = registry.get("greetings")
    return info is not None and info.enabled and not await disabled.module_off(chat_id, "greetings")


@router.chat_member(_joined)
async def on_member_joined(event: ChatMemberUpdated, bot: Bot) -> None:
    user = event.new_chat_member.user
    if user.id == bot.id:
        return
    logging_on = await _has_log(event.chat.id)
    greeting = not user.is_bot and await _greetings_here(event.chat.id)
    if not logging_on and not greeting:
        return
    if not await memberevents.no_service_message("join", event.chat.id, user.id):
        return  # the service message came and has done it
    if logging_on:
        await _log_member(bot, event.chat, "JOINED", user)
    if not greeting:
        return
    if await banguard.applies(bot, event.chat.id, user.id):
        return
    if antiraid.was_removed(event.chat.id, user.id):
        return
    try:
        await captchas.hold(bot, event.chat, user, None, 0)
    except Exception:
        log.exception("Starting a captcha for %s in chat %s failed", user.id, event.chat.id)
    await _greet(_made_up_message(bot, event.chat), [user], await greetings.get(event.chat.id))


@router.chat_member(_left)
async def on_member_left(event: ChatMemberUpdated, bot: Bot) -> None:
    user = event.old_chat_member.user
    if user.id == bot.id:
        return
    logging_on = await _has_log(event.chat.id)
    greeting = not user.is_bot and await _greetings_here(event.chat.id)
    if not logging_on and not greeting:
        return
    if not await memberevents.no_service_message("leave", event.chat.id, user.id):
        return
    left_on_own = event.from_user is not None and event.from_user.id == user.id
    if logging_on and left_on_own:
        await _log_member(bot, event.chat, "LEFT", user)
    if not greeting:
        return
    try:
        await captchas.drop_user(bot, event.chat.id, user.id)
    except Exception:
        log.exception("Cleaning up the captcha of %s in chat %s failed", user.id, event.chat.id)
    await _say_goodbye(_made_up_message(bot, event.chat), user, await greetings.get(event.chat.id), left_on_own)
