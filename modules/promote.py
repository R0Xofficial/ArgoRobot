"""Promote, demote, titles and tags"""

from aiogram import Router
from aiogram.exceptions import TelegramBadRequest, TelegramForbiddenError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import RIGHTS_MESSAGE, ModerationError, NotAllowed, perform
from core.roles import Role
from core.targets import Target, TargetError, replied_message, resolve_target
from core.text import esc, user_link, user_ref

ABOUT = """
Manage admins without digging through Telegram's menus.

You need the right to add new admins, and so do I: I can only hand out permissions I have myself. Telegram lets bots change the title of only those admins the bot promoted, so /title and /demote work only on admins promoted by me.

Name the target by replying to their message, or with a @username or a numeric ID.
"""

HELP_WHO = "admins"

HELP = [
    ("/promote <user> [title]", "Make a user an admin with the usual permissions. Text after the user becomes their title, like /promote @name Guy."),
    ("/fullPromote <user> [title]", "Make a user an admin with every permission except staying anonymous. A title can follow the user too."),
    ("/demote <user>", "Take admin rights away from an admin I promoted."),
    ("/title <user> <text>", "Set a custom title (up to 16 characters, no emoji) on an admin I promoted. Without text the title is cleared. Admins can change their own."),
    ("/tag <user> <text>", "Set a custom tag (up to 16 characters) on a member. Without text the tag is cleared."),
]

router = Router(name="promote")

MAX_LENGTH = 16  # Telegram's limit for titles and tags

# Admin rights this module hands out, with the names shown to people.
RIGHT_NAMES = {
    "can_manage_chat": "manage chat",
    "can_change_info": "change info",
    "can_delete_messages": "delete messages",
    "can_invite_users": "invite users",
    "can_restrict_members": "restrict members",
    "can_pin_messages": "pin messages",
    "can_manage_video_chats": "manage video chats",
    "can_manage_topics": "manage topics",
    "can_manage_tags": "manage tags",
    "can_promote_members": "add admins",
}
FULL_RIGHTS = tuple(RIGHT_NAMES)
BASIC_RIGHTS = tuple(right for right in FULL_RIGHTS if right != "can_promote_members")

# Everything Telegram has to be told "no" about for a demotion to take.
ALL_RIGHTS_OFF = (
    *FULL_RIGHTS,
    "can_post_stories",
    "can_edit_stories",
    "can_delete_stories",
    "can_send_welcome_messages",
    "is_anonymous",
)


async def _call(call):
    """
    Await a promotion call. Unlike moderation.perform, "admin required" is not read
    as "I lack the rights" (I checked those): it means the admin was not promoted by me.
    """
    try:
        return await call
    except TelegramForbiddenError:
        raise ModerationError(RIGHTS_MESSAGE) from None
    except TelegramBadRequest as exc:
        if any(key in str(exc).lower() for key in ("user not found", "participant_id_invalid", "user_not_participant")):
            raise ModerationError("That user isn't in this chat.") from None
        raise


def _status(member) -> str:
    return getattr(member.status, "value", member.status)


def _has(member, right: str) -> bool:
    """Whether a chat member holds an admin right (the creator holds all of them)."""
    return _status(member) == "creator" or getattr(member, right, False) is True


async def _member(message: Message, user_id: int):
    member = await moderation.get_member(message.bot, message.chat.id, user_id)
    if member is None:
        raise ModerationError("I couldn't check that user in this chat.")
    return member


async def _begin(message: Message) -> object | None:
    """
    Common start: groups only, not anonymous, and the person must be the owner of
    the chat or an admin allowed to add admins. The bot's staff has no special
    say here: who runs a chat is for the chat to decide.
    Returns the person's membership, or None after answering.
    """
    try:
        if message.chat.type not in moderation.GROUP_TYPES:
            raise ModerationError("This command only works in groups.")
        if moderation.actor_of(message).anonymous:
            raise NotAllowed("Send this command as yourself, not as an anonymous admin.")
        member = await _member(message, message.from_user.id)
        if not _has(member, "can_promote_members"):
            raise NotAllowed("You need the right to add new admins to use this command.")
        return member
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return None


async def _bot_member(message: Message, *needed: str):
    me = await message.bot.me()
    member = await _member(message, me.id)
    for right in needed:
        if not _has(member, right):
            raise ModerationError(f"I need the admin right to {RIGHT_NAMES.get(right, right)} for that.")
    return member


async def _target(message: Message, command: CommandObject, *, self_fallback: bool = False) -> Target:
    """
    The user a command is aimed at. With `self_fallback`, a command given with
    text but no user (no reply, no @username or id) is aimed at the person
    writing it: `/title Boss`.
    """
    try:
        return await resolve_target(message, command, allow_bots=True)
    except TargetError:
        args = (command.args or "").strip()
        looks_like_user = args.startswith("@") or args.split(" ")[0].lstrip("-").isdigit()
        if self_fallback and replied_message(message) is None and not looks_like_user:
            user = message.from_user
            return Target(user.id, user.full_name, extra=args)
        if self_fallback and not args and replied_message(message) is None:
            user = message.from_user
            return Target(user.id, user.full_name)
        raise


def _rights_text(rights: list[str]) -> str:
    return ", ".join(RIGHT_NAMES[right] for right in rights) or "none"


# --- /promote, /fullPromote -------------------------------------------------------

@router.message(cmd("promote", "fullPromote", "fullpromote"))
async def cmd_promote(message: Message, command: CommandObject) -> None:
    full = command.command.lower() == "fullpromote"
    actor_member = await _begin(message)
    if actor_member is None:
        return
    bot = message.bot
    actor = moderation.actor_of(message)

    try:
        target = await _target(message, command)
        title = target.extra.strip()
        if len(title) > MAX_LENGTH:
            raise ModerationError(f"A title can have at most {MAX_LENGTH} characters.")
        await moderation.check_target(bot, message.chat, target, actor, None, punitive=False)
        bot_member = await _bot_member(message, "can_promote_members")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status == "creator":
            raise ModerationError("I can't change the owner of the chat.")
        if status in ("left", "kicked") or (status == "restricted" and not getattr(target_member, "is_member", True)):
            raise ModerationError("That user isn't in this chat.")

        # Nobody can hand out more than they hold themselves, and neither can I.
        wanted = FULL_RIGHTS if full else BASIC_RIGHTS
        rights = [r for r in wanted if _has(bot_member, r) and _has(actor_member, r)]
        if not rights:
            raise ModerationError("You and I have no admin rights in common to give.")

        await _call(
            bot.promote_chat_member(
                message.chat.id, target.user_id, is_anonymous=False, **{right: True for right in rights}
            )
        )
    except (TargetError, ModerationError) as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        await reply(message, _promotion_problem(exc))
        return

    # /promote @user Guy: the rest of the line is the new admin's title.
    title_line = ""
    if title:
        try:
            await _call(bot.set_chat_administrator_custom_title(message.chat.id, target.user_id, title))
            title_line = f"\nTitle: <b>{esc(title)}</b>"
        except (TelegramBadRequest, ModerationError) as exc:
            reason = "A title can't contain emoji." if "emoji" in str(exc).lower() else "I couldn't set the title."
            title_line = f"\nTitle not set: {reason}"

    kind = "to admin with every permission I can give" if full else "to admin"
    await moderation.log_action(
        message, "PROMOTED", actor, target, "", ("Rights", esc(_rights_text(rights))), *((("Title", esc(title)),) if title else ())
    )
    await reply(
        message,
        f"{user_link(target.user_id, target.name)} was promoted {kind}{moderation.by(actor)}\n"
        f"Rights: {_rights_text(rights)}.{title_line}" + moderation.staff_note(actor),
    )


def _promotion_problem(exc: TelegramBadRequest) -> str:
    problem = str(exc).lower()
    if "administrator" in problem or "admin_rank" in problem or "chat_admin_required" in problem:
        return "I can only change admins that I promoted myself."
    if "bot" in problem and "can't" in problem:
        return "Telegram doesn't allow that for this user."
    raise exc


# --- /demote --------------------------------------------------------------------------

@router.message(cmd("demote"))
async def cmd_demote(message: Message, command: CommandObject) -> None:
    if await _begin(message) is None:
        return
    bot = message.bot
    actor = moderation.actor_of(message)

    try:
        target = await _target(message, command)
        await moderation.check_target(bot, message.chat, target, actor, None, punitive=False)
        await _bot_member(message, "can_promote_members")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status == "creator":
            raise ModerationError("I can't demote the owner of the chat.")
        if status != "administrator":
            raise ModerationError("That user isn't an admin.")
        await _call(bot.promote_chat_member(message.chat.id, target.user_id, **{r: False for r in ALL_RIGHTS_OFF}))
    except (TargetError, ModerationError) as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        await reply(message, _promotion_problem(exc))
        return

    await moderation.log_action(message, "DEMOTED", actor, target)
    await reply(
        message,
        f"{user_link(target.user_id, target.name)} was demoted{moderation.by(actor)}" + moderation.staff_note(actor),
    )


# --- /title ---------------------------------------------------------------------------

@router.message(cmd("title"))
async def cmd_title(message: Message, command: CommandObject) -> None:
    """Custom title of an admin. Admins may change their own; changing another's needs the right to add admins."""
    bot = message.bot
    actor = moderation.actor_of(message)
    try:
        if message.chat.type not in moderation.GROUP_TYPES:
            raise ModerationError("This command only works in groups.")
        if actor.anonymous:
            raise NotAllowed("Send this command as yourself, not as an anonymous admin.")
        target = await _target(message, command, self_fallback=True)
        text = target.extra.strip()
        if len(text) > MAX_LENGTH:
            raise ModerationError(f"A title can have at most {MAX_LENGTH} characters.")

        actor_member = await _member(message, actor.id)
        own = target.user_id == actor.id
        if own:
            if _status(actor_member) not in ("administrator", "creator"):
                raise NotAllowed("Only admins can have a title.")
        else:
            await moderation.check_target(bot, message.chat, target, actor, None, punitive=False)
            if not _has(actor_member, "can_promote_members"):
                raise NotAllowed("You need the right to add new admins to change someone else's title.")
        await _bot_member(message, "can_promote_members")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status == "creator":
            raise ModerationError("The owner of the chat sets their own title in Telegram.")
        if status != "administrator":
            raise ModerationError("That user isn't an admin.")
        await _call(bot.set_chat_administrator_custom_title(message.chat.id, target.user_id, text))
    except (TargetError, ModerationError) as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if "emoji" in problem:
            await reply(message, "A title can't contain emoji.")
        elif "admin" in problem:
            await reply(message, "I can only set the title of admins that I promoted myself.")
        else:
            raise
        return

    who = user_link(target.user_id, target.name)
    if text:
        await reply(message, f"{who} was given the title <b>{esc(text)}</b>{moderation.by(actor)}")
    else:
        await reply(message, f"The title of {who} was cleared{moderation.by(actor)}")


# --- /tag -------------------------------------------------------------------------------

async def _may_edit_own_tag(message: Message, member) -> bool:
    status = _status(member)
    if status in ("administrator", "creator"):
        return True
    if status == "restricted":
        return getattr(member, "can_edit_tag", False) is True
    try:
        chat = await message.bot.get_chat(message.chat.id)
    except TelegramBadRequest:
        return False
    return getattr(getattr(chat, "permissions", None), "can_edit_tag", False) is True


@router.message(cmd("tag"))
async def cmd_tag(message: Message, command: CommandObject) -> None:
    """Custom tag of a member. Anyone may set their own where the chat allows it; others' needs the right to manage tags."""
    bot = message.bot
    actor = moderation.actor_of(message)
    try:
        if message.chat.type not in moderation.GROUP_TYPES:
            raise ModerationError("This command only works in groups.")
        if actor.anonymous:
            raise NotAllowed("Send this command as yourself, not as an anonymous admin.")
        target = await _target(message, command, self_fallback=True)
        text = target.extra.strip()
        if len(text) > MAX_LENGTH:
            raise ModerationError(f"A tag can have at most {MAX_LENGTH} characters.")

        actor_member = await _member(message, actor.id)
        own = target.user_id == actor.id
        if own:
            if not await _may_edit_own_tag(message, actor_member):
                raise NotAllowed("This chat doesn't let members edit their own tag.")
        else:
            await moderation.check_target(bot, message.chat, target, actor, None, punitive=False)
            if not _has(actor_member, "can_manage_tags"):
                raise NotAllowed("You need the right to manage tags to change someone else's tag.")
        await _bot_member(message, "can_manage_tags")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status in ("administrator", "creator"):
            raise ModerationError("Admins have a title instead. Use /title.")
        if status in ("left", "kicked") or (status == "restricted" and not getattr(target_member, "is_member", True)):
            raise ModerationError("That user isn't in this chat.")
        await perform(bot.set_chat_member_tag(message.chat.id, target.user_id, text or None))
    except (TargetError, ModerationError) as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        if "emoji" in str(exc).lower():
            await reply(message, "A tag can't contain emoji.")
            return
        raise

    who = user_link(target.user_id, target.name)
    if text:
        await reply(message, f"{who} was given the tag <b>{esc(text)}</b>{moderation.by(actor)}")
    else:
        await reply(message, f"The tag of {who} was cleared{moderation.by(actor)}")
