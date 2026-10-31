"""Welcome and goodbye messages"""

import logging
from contextlib import suppress
from datetime import datetime, timedelta, timezone

from aiogram import F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Chat, ChatPermissions, Message

from core import approvals, captchas, disabled, gbans, greetings, immunity, moderation, registry, roles, sender, staffgreet
from core.filters import cmd
from core.help_index import Section
from core.helpers import reply
from core.markup import MarkupError
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.targets import replied_message
from core.text import esc, SWITCH as _ANSWERS

log = logging.getLogger(__name__)

ABOUT = """
Greet people when they join the chat and say goodbye when they leave. Welcomes and goodbyes are both on by default. Until you set your own, I use a default message for each.

A message can be written after the command or taken from a message you reply to, media included. It can be formatted, address people by name, carry buttons and vary at random, as described below. Everything that changes these settings is for chat admins with the right to change chat info.

Welcomes are sent when I see someone join, and goodbyes only when someone leaves on their own: people who are removed get none. Bots are not greeted.

My staff (Support, Sudo, Developer, Owner) are different: they are always welcomed when they join and seen off when they leave, with a message of their role, whatever the chat's settings are. They are not welcome-muted or tested with a captcha.
"""

S_WELCOME, S_GOODBYE = Section("Welcome Commands"), Section("Goodbye Commands")

HELP = [
    ("/welcome", "Show whether welcomes are on and the current welcome message.", "admins", S_WELCOME),
    ("/welcome noformat", "Show the welcome message exactly as it was written, without formatting.", "admins", S_WELCOME),
    ("/welcome <on|off>", "Turn welcomes on or off.", "admins", S_WELCOME),
    ("/setwelcome <reply or text>", "Set a custom welcome message. Reply to a message to use it, media included.", "admins", S_WELCOME),
    ("/resetwelcome", "Go back to the default welcome message.", "admins", S_WELCOME),
    ("/goodbye", "Show whether goodbyes are on and the current goodbye message.", "admins", S_GOODBYE),
    ("/goodbye noformat", "Show the goodbye message exactly as it was written, without formatting.", "admins", S_GOODBYE),
    ("/goodbye <on|off>", "Turn goodbyes on or off.", "admins", S_GOODBYE),
    ("/setgoodbye <reply or text>", "Set a custom goodbye message. Reply to a message to use it, media included.", "admins", S_GOODBYE),
    ("/resetgoodbye", "Go back to the default goodbye message.", "admins", S_GOODBYE),
    ("/welcomemute <on|off>", "Stop new members from sending media, stickers and polls for their first 24 hours. They can still write. Needs the right to restrict users. Approved users are skipped.", "admins", S_WELCOME),
    ("/cleanwelcome <on|off>", "Delete my previous welcome message each time I send a new one.", "admins", S_WELCOME),
    ("/cleanservice <on|off>", "Delete the \"joined\" and \"left\" service messages. I need the right to delete messages.", "admins", S_WELCOME),
]

HELP_NOTES = [
    (
        "Formatting",
        """
        <b>bold</b>  <i>italic</i>  <u>underline</u>  <s>strike</s>  <tg-spoiler>spoiler</tg-spoiler>
        <code>code</code>  <pre>pre</pre>  <blockquote>quote</blockquote>  <a href="https://link">text</a>
        The formatting menu of Telegram works as well: select the text and pick Bold, Italic, Link...
        Anything that is not one of these tags is shown as typed.
        """,
    ),
    (
        "Fillings",
        """
        Replaced by facts about the person who joined or left:
        {first} {last} {fullname} {username} {mention} {id} {chatname} {count}
        {username} falls back to a mention for people without one. {count} is the number of members.
        """,
    ),
    (
        "Options",
        """
        Put these anywhere in the text; they are removed and change how the message is sent:
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
        """,
    ),
    (
        "Examples",
        """
        /setwelcome Hello {mention}, welcome to {chatname}! [Rules](buttonurl://example.com/rules)
        /setgoodbye {first} left. We have {count} members now.
        /welcomemute on
        """,
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
    return "Yes" if value else "No"


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
        f"• <b>Enabled:</b> <code>{_yes_no(settings[f'{kind}_on'])}</code>",
        f"• <b>Message:</b> <code>{'Custom' if custom else 'Default'}</code>",
    ]
    if kind == "welcome":
        head += [
            f"• <b>Welcome mute:</b> <code>{_yes_no(settings['welcome_mute'])}</code>",
            f"• <b>Clean welcome:</b> <code>{_yes_no(settings['clean_welcome'])}</code>",
            f"• <b>Clean service:</b> <code>{_yes_no(settings['clean_service'])}</code>",
        ]
    await reply(message, "\n".join(head))
    await sender.send(message, message.from_user, content, media_type, file_id, raw=argument == "noformat", label=kind)


async def _command(message: Message, command: CommandObject, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    argument = (command.args or "").strip().lower()
    if not argument or argument == "noformat":
        await _show(message, role, kind, argument)
        return
    if argument not in _ANSWERS:
        await reply(message, f"Use /{kind}, /{kind} noformat or /{kind} on|off.")
        return
    if not await _begin(message, role, change=True):
        return
    enabled = _ANSWERS[argument]
    plural = "Welcomes" if kind == "welcome" else "Goodbyes"
    if bool((await greetings.get(message.chat.id))[f"{kind}_on"]) == enabled:
        await reply(message, f"{plural} are already <b>{'on' if enabled else 'off'}</b>.")
        return
    await greetings.update(message.chat.id, **{f"{kind}_on": enabled})
    await reply(message, f"{plural} are now <b>{'on' if enabled else 'off'}</b>.")


async def _set(message: Message, command: CommandObject, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
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
        await reply(message, f"Tell me what to say: write the text after /set{kind}, or reply to a message.")
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
    await reply(message, f"The {kind} message is saved. Use /{kind} to see it.")


async def _reset(message: Message, role: Role | None, kind: str) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    if not await _begin(message, role, change=True):
        return
    await greetings.update(
        message.chat.id,
        **{f"{kind}_content": None, f"{kind}_media_type": None, f"{kind}_media_file_id": None},
    )
    await reply(message, f"The {kind} message is back to the default.")


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
        await reply(message, "This command only works in groups.")
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
        await reply(message, f"This is already <b>{'on' if enabled else 'off'}</b>.")
        return
    await greetings.update(message.chat.id, **{column: enabled})
    await reply(message, text(enabled))


@router.message(cmd("welcomemute"))
async def cmd_welcomemute(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch(
        message, command, role, "welcome_mute", "Use /welcomemute on or /welcomemute off.",
        lambda on: f"New members <b>can't send media</b> for their first {MUTE_HOURS} hours. They can still write."
        if on
        else "New members can send anything right away.",
    )


@router.message(cmd("cleanwelcome"))
async def cmd_cleanwelcome(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch(
        message, command, role, "clean_welcome", "Use /cleanwelcome on or /cleanwelcome off.",
        lambda on: "My previous welcome message will be <b>deleted</b> when I send a new one."
        if on
        else "Old welcome messages will be <b>kept</b>.",
    )


@router.message(cmd("cleanservice"))
async def cmd_cleanservice(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch(
        message, command, role, "clean_service", "Use /cleanservice on or /cleanservice off.",
        lambda on: "The \"joined\" and \"left\" service messages will be <b>deleted</b>."
        if on
        else "Service messages will be <b>kept</b>.",
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
    await sender.send(message, user, staffgreet.get(role, "welcome"), None, None, label="staff welcome message")


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
            sent = await sender.send(message, user, content, media_type, file_id, label="welcome message", extra_button=button)
            if sent is not None:
                last_sent = sent.message_id
                if button is not None:
                    await captchas.mark_welcomed(message.chat.id, user.id)
        if last_sent is not None and settings["clean_welcome"]:
            await greetings.update(message.chat.id, last_welcome_id=last_sent)


@router.message(F.new_chat_members)
async def on_join(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        return
    settings = await greetings.get(message.chat.id)
    bot_id = message.bot.id
    people = []
    for user in message.new_chat_members:
        if user.is_bot or user.id == bot_id or await gbans.is_gbanned(user.id):
            continue  # bots are not greeted, and a globally banned user is about to be removed
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
    # The sender of the service message is the person who left, or whoever removed them.
    left_on_own = message.from_user is not None and message.from_user.id == left.id
    try:
        if left_on_own and not left.is_bot and left.id != message.bot.id:
            role = await roles.get_role(left.id)
            if role is not None:  # staff are always seen off, with the goodbye of their role
                await sender.send(message, left, staffgreet.get(role, "goodbye"), None, None, label="staff goodbye message")
            elif settings["goodbye_on"]:
                content, media_type, file_id = greetings.message_of(settings, "goodbye")
                await sender.send(message, left, content, media_type, file_id, label="goodbye message")
    finally:
        if settings["clean_service"]:
            await _delete(message)
