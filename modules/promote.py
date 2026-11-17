"""Promote, demote, titles and tags"""

from types import SimpleNamespace

from aiogram import Router
from aiogram.exceptions import TelegramBadRequest, TelegramForbiddenError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import chatsettings, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import RIGHTS_MESSAGE, ModerationError, NotAllowed, perform
from core.roles import Role
from core.targets import Target, TargetError, replied_message, resolve_target
from core.text import esc, user_link, user_ref
from core.i18n import tr

ABOUT = "promote.about"

HELP_WHO = "common.admins"

HELP = [
    ("/promote <user> [title]", "promote.help.make_a_user_an_admin"),
    ("/fullPromote <user> [title]", "promote.help.make_a_user_an_admin_2"),
    ("/demote <user>", "promote.help.take_admin_rights_away_from"),
    ("/title <user> <text>", "promote.help.set_a_custom_title_up"),
    ("/tag <user> <text>", "promote.help.set_a_custom_tag_up"),
]

router = Router(name="promote")

MAX_LENGTH = 16  # Telegram's limit for titles and tags

# Admin rights this module hands out, with the names shown to people.
RIGHT_NAMES = {
    "can_manage_chat": "promote.right_names.can_manage_chat",
    "can_change_info": "promote.right_names.can_change_info",
    "can_delete_messages": "promote.right_names.can_delete_messages",
    "can_invite_users": "common.invite_users",
    "can_restrict_members": "promote.right_names.can_restrict_members",
    "can_pin_messages": "promote.right_names.can_pin_messages",
    "can_manage_video_chats": "promote.right_names.can_manage_video",
    "can_manage_topics": "promote.right_names.can_manage_topics",
    "can_manage_tags": "promote.right_names.can_manage_tags",
    "can_promote_members": "promote.right_names.can_promote_members",
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
        raise ModerationError(tr(RIGHTS_MESSAGE)) from None
    except TelegramBadRequest as exc:
        if any(key in str(exc).lower() for key in ("user not found", "participant_id_invalid", "user_not_participant")):
            raise ModerationError(tr("common.that_user_isn_t_in_this")) from None
        raise


def _status(member) -> str:
    return getattr(member.status, "value", member.status)


def _has(member, right: str) -> bool:
    """Whether a chat member holds an admin right (the creator holds all of them)."""
    return _status(member) == "creator" or getattr(member, right, False) is True


async def _member(message: Message, user_id: int):
    member = await moderation.get_member(message.bot, message.chat.id, user_id)
    if member is None:
        raise ModerationError(tr("promote.i_couldn_t_check_that_user"))
    return member


# What the bot's staff counts as in the chat: somebody holding every admin right.
_STAFF_MEMBER = SimpleNamespace(status="creator", staff=True)


async def _staff_may_help(message: Message, role: Role | None) -> bool:
    """Whether this person acts as the bot's staff: Sudo and up, while the chat's owner has not turned that off."""
    return role is not None and role >= moderation.STAFF_ROLE and await chatsettings.staff_help(message.chat.id)


async def _actor_member(message: Message, role: Role | None, right: str):
    """
    The person's membership when they hold `right`, the staff stand-in when they act as the
    bot's staff, else whatever they are (possibly None) for the caller to refuse.
    """
    try:
        member = await _member(message, message.from_user.id)
    except ModerationError:
        member = None
    if member is not None and _has(member, right):
        return member
    if await _staff_may_help(message, role):
        return _STAFF_MEMBER
    return member


def _mark_staff(actor, member) -> None:
    actor.staff = getattr(member, "staff", False)


async def _begin(message: Message, role: Role | None = None) -> object | None:
    """
    Common start: groups only, not anonymous, and the person must be the owner of
    the chat, an admin allowed to add admins, or one of the bot's staff (Sudo and up)
    unless the chat's owner turned their help off.
    Returns the person's membership, or None after answering.
    """
    try:
        if message.chat.type not in moderation.GROUP_TYPES:
            raise ModerationError(tr("common.this_command_only_works_in_groups"))
        if moderation.actor_of(message).anonymous:
            raise NotAllowed(tr("common.send_this_command_as_yourself_not"))
        member = await _actor_member(message, role, "can_promote_members")
        if member is None:
            raise ModerationError(tr("common.i_couldn_t_check_your_rights"))
        if not _has(member, "can_promote_members"):
            raise NotAllowed(tr("promote.you_need_the_right_to_add"))
        return member
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return None


async def _bot_member(message: Message, *needed: str):
    me = await message.bot.me()
    member = await _member(message, me.id)
    for right in needed:
        if not _has(member, right):
            raise ModerationError(tr("promote.i_need_the_admin_right_to", get=tr(RIGHT_NAMES.get(right, right))))
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
    return ", ".join(tr(RIGHT_NAMES[right]) for right in rights) or tr("promote.none")


# --- /promote, /fullPromote -------------------------------------------------------

@router.message(cmd("promote", "fullPromote", "fullpromote"))
async def cmd_promote(message: Message, command: CommandObject, role: Role | None = None) -> None:
    full = command.command.lower() == "fullpromote"
    actor_member = await _begin(message, role)
    if actor_member is None:
        return
    bot = message.bot
    actor = moderation.actor_of(message)
    _mark_staff(actor, actor_member)

    try:
        target = await _target(message, command)
        title = target.extra.strip()
        if len(title) > MAX_LENGTH:
            raise ModerationError(tr("promote.a_title_can_have_at_most", MAX_LENGTH=MAX_LENGTH))
        await moderation.check_target(bot, message.chat, target, actor, None, punitive=False, allow_self=True)
        bot_member = await _bot_member(message, "can_promote_members")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status == "creator":
            raise ModerationError(tr("promote.i_can_t_change_the_owner"))
        if status in ("left", "kicked") or (status == "restricted" and not getattr(target_member, "is_member", True)):
            raise ModerationError(tr("common.that_user_isn_t_in_this"))

        # Nobody can hand out more than they hold themselves, and neither can I.
        wanted = FULL_RIGHTS if full else BASIC_RIGHTS
        rights = [r for r in wanted if _has(bot_member, r) and _has(actor_member, r)]
        if not rights:
            raise ModerationError(tr("promote.you_and_i_have_no_admin"))

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
            title_line = tr("promote.title", title=esc(title))
        except (TelegramBadRequest, ModerationError) as exc:
            reason = tr("promote.a_title_can_t_contain_emoji") if "emoji" in str(exc).lower() else tr("promote.i_couldn_t_set_the_title")
            title_line = tr("promote.title_not_set", reason=reason)

    kind = tr("promote.to_admin_with_full_rights") if full else tr("promote.to_admin")
    await moderation.log_action(
        message, "PROMOTED", actor, target, "", (tr("promote.rights"), esc(_rights_text(rights))), *(((tr("common.title"), esc(title)),) if title else ())
    )
    await reply(
        message,
        tr("promote.was_promoted", user_id=user_link(target.user_id, target.name), kind=kind, by=moderation.by(actor))
        + title_line + moderation.staff_note(actor),
    )


def _promotion_problem(exc: TelegramBadRequest) -> str:
    problem = str(exc).lower()
    if "administrator" in problem or "admin_rank" in problem or "chat_admin_required" in problem:
        return tr("promote.i_can_only_change_admins_that")
    if "bot" in problem and "can't" in problem:
        return tr("promote.telegram_doesn_t_allow_that_for")
    raise exc


# --- /demote --------------------------------------------------------------------------

@router.message(cmd("demote"))
async def cmd_demote(message: Message, command: CommandObject, role: Role | None = None) -> None:
    actor_member = await _begin(message, role)
    if actor_member is None:
        return
    bot = message.bot
    actor = moderation.actor_of(message)
    _mark_staff(actor, actor_member)

    try:
        target = await _target(message, command)
        await moderation.check_target(bot, message.chat, target, actor, None, punitive=False, allow_self=True)
        await _bot_member(message, "can_promote_members")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status == "creator":
            raise ModerationError(tr("promote.i_can_t_demote_the_owner"))
        if status != "administrator":
            raise ModerationError(tr("promote.that_user_isn_t_an_admin"))
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
        tr("promote.was_demoted", user_id=user_link(target.user_id, target.name), by=moderation.by(actor)) + moderation.staff_note(actor),
    )


# --- /title ---------------------------------------------------------------------------

@router.message(cmd("title"))
async def cmd_title(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """Custom title of an admin. Admins may change their own; changing another's needs the right to add admins."""
    bot = message.bot
    actor = moderation.actor_of(message)
    try:
        if message.chat.type not in moderation.GROUP_TYPES:
            raise ModerationError(tr("common.this_command_only_works_in_groups"))
        if actor.anonymous:
            raise NotAllowed(tr("common.send_this_command_as_yourself_not"))
        target = await _target(message, command, self_fallback=True)
        text = target.extra.strip()
        if len(text) > MAX_LENGTH:
            raise ModerationError(tr("promote.a_title_can_have_at_most", MAX_LENGTH=MAX_LENGTH))

        own = target.user_id == actor.id
        if own:
            actor_member = await _member(message, actor.id)
            if _status(actor_member) not in ("administrator", "creator"):
                raise NotAllowed(tr("promote.only_admins_can_have_a_title"))
        else:
            actor_member = await _actor_member(message, role, "can_promote_members")
            if actor_member is None:
                raise ModerationError(tr("common.i_couldn_t_check_your_rights"))
            await moderation.check_target(bot, message.chat, target, actor, None, punitive=False, allow_self=True)
            if not _has(actor_member, "can_promote_members"):
                raise NotAllowed(tr("promote.you_need_the_right_to_add_2"))
            _mark_staff(actor, actor_member)
        await _bot_member(message, "can_promote_members")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status == "creator":
            raise ModerationError(tr("promote.the_owner_of_the_chat_sets"))
        if status != "administrator":
            raise ModerationError(tr("promote.that_user_isn_t_an_admin"))
        await _call(bot.set_chat_administrator_custom_title(message.chat.id, target.user_id, text))
    except (TargetError, ModerationError) as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        problem = str(exc).lower()
        if "emoji" in problem:
            await reply(message, tr("promote.a_title_can_t_contain_emoji"))
        elif "admin" in problem:
            await reply(message, tr("promote.i_can_only_set_the_title"))
        else:
            raise
        return

    who = user_link(target.user_id, target.name)
    if text:
        await reply(message, tr("promote.was_given_the_title", who=who, text=esc(text), by=moderation.by(actor)))
    else:
        await reply(message, tr("promote.the_title_of_was_cleared", who=who, by=moderation.by(actor)))


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
async def cmd_tag(message: Message, command: CommandObject, role: Role | None = None) -> None:
    """Custom tag of a member. Anyone may set their own where the chat allows it; others' needs the right to manage tags."""
    bot = message.bot
    actor = moderation.actor_of(message)
    try:
        if message.chat.type not in moderation.GROUP_TYPES:
            raise ModerationError(tr("common.this_command_only_works_in_groups"))
        if actor.anonymous:
            raise NotAllowed(tr("common.send_this_command_as_yourself_not"))
        target = await _target(message, command, self_fallback=True)
        text = target.extra.strip()
        if len(text) > MAX_LENGTH:
            raise ModerationError(tr("promote.a_tag_can_have_at_most", MAX_LENGTH=MAX_LENGTH))

        own = target.user_id == actor.id
        if own:
            actor_member = await _member(message, actor.id)
            if not await _may_edit_own_tag(message, actor_member):
                raise NotAllowed(tr("promote.this_chat_doesn_t_let_members"))
        else:
            actor_member = await _actor_member(message, role, "can_manage_tags")
            if actor_member is None:
                raise ModerationError(tr("common.i_couldn_t_check_your_rights"))
            await moderation.check_target(bot, message.chat, target, actor, None, punitive=False, allow_self=True)
            if not _has(actor_member, "can_manage_tags"):
                raise NotAllowed(tr("promote.you_need_the_right_to_manage"))
            _mark_staff(actor, actor_member)
        await _bot_member(message, "can_manage_tags")
        target_member = await _member(message, target.user_id)
        status = _status(target_member)
        if status in ("administrator", "creator"):
            raise ModerationError(tr("promote.admins_have_a_title_instead_use"))
        if status in ("left", "kicked") or (status == "restricted" and not getattr(target_member, "is_member", True)):
            raise ModerationError(tr("common.that_user_isn_t_in_this"))
        await perform(bot.set_chat_member_tag(message.chat.id, target.user_id, text or None))
    except (TargetError, ModerationError) as exc:
        await moderation.deny(message, exc)
        return
    except TelegramBadRequest as exc:
        if "emoji" in str(exc).lower():
            await reply(message, tr("promote.a_tag_can_t_contain_emoji"))
            return
        raise

    who = user_link(target.user_id, target.name)
    if text:
        await reply(message, tr("promote.was_given_the_tag", who=who, text=esc(text), by=moderation.by(actor)))
    else:
        await reply(message, tr("promote.the_tag_of_was_cleared", who=who, by=moderation.by(actor)))
