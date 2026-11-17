"""Zombies: remove deleted accounts from a chat"""

import asyncio
import logging

from aiogram import Router
from aiogram.exceptions import TelegramAPIError, TelegramRetryAfter
from aiogram.types import Message

from core import chats, moderation, mtproto
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc
from core.i18n import plural_tag, tr

log = logging.getLogger(__name__)

ABOUT = "zombies.about"

HELP_WHO = "common.admins"

HELP = [
    ("/zombies", "zombies.help.find_the_deleted_accounts_in"),
]

router = Router(name="zombies")

DELETED_NAME = "Deleted Account"
PAUSE = 0.06  # between Telegram calls, to stay far from the flood limits
MAX_CHECKED = 5000

_running: set[int] = set()
_tasks: set[asyncio.Task] = set()


def _is_deleted(member) -> bool:
    user = getattr(member, "user", None)
    return user is not None and (getattr(user, "first_name", None) or "").strip().lower() == DELETED_NAME.lower()


def _present(member) -> bool:
    status = str(getattr(member.status, "value", member.status))
    if status == "restricted":
        return getattr(member, "is_member", True) is not False
    return status == "member"


async def _scan(bot, chat_id: int, user_ids: list[int]) -> list[int]:
    """Which of the people I know are deleted accounts still in the chat, going by what Telegram says about each."""
    deleted: list[int] = []
    unreadable = gone = 0
    for user_id in user_ids:
        try:
            member = await bot.get_chat_member(chat_id, user_id)
        except TelegramRetryAfter as exc:
            await asyncio.sleep(exc.retry_after)
            continue
        except TelegramAPIError:
            unreadable += 1  # e.g. an account Telegram no longer returns
            await asyncio.sleep(PAUSE)
            continue
        if not _present(member):
            gone += 1  # known to me, but no longer in the chat
        user = getattr(member, "user", None)
        name = (getattr(user, "first_name", None) or "").strip()
        if user is not None and not _is_deleted(member) and (not name or "delet" in name.lower()):
            log.info("Zombies: odd account %s in %s: name %r, status %s", user_id, chat_id, name, getattr(member.status, "value", member.status))
        if _is_deleted(member) and _present(member):
            deleted.append(user_id)
        await asyncio.sleep(PAUSE)
    if unreadable or gone:
        log.info("Zombies in %s: checked %s, couldn't look up %s, no longer in the chat %s, found %s", chat_id, len(user_ids), unreadable, gone, len(deleted))
    return deleted


async def _sweep(message: Message, progress: Message, user_ids: list[int]) -> None:
    bot, chat_id = message.bot, message.chat.id
    found = removed = failed = 0
    reasons: dict[str, int] = {}  # why removing failed, and how many times
    try:
        listed = await mtproto.members(chat_id)
        if listed is not None:
            deleted = [user_id for user_id, is_deleted in listed if is_deleted]
            log.info("Zombies in %s: %s members listed, %s deleted", chat_id, len(listed), len(deleted))
        else:
            deleted = await _scan(bot, chat_id, user_ids)
        found = len(deleted)
        for user_id in deleted:
            try:
                await perform(bot.ban_chat_member(chat_id, user_id))
                await perform(bot.unban_chat_member(chat_id, user_id, only_if_banned=True))
                removed += 1
            except (ModerationError, TelegramAPIError) as exc:
                failed += 1
                reason = str(exc) if isinstance(exc, ModerationError) else getattr(exc, "message", None) or str(exc)
                reasons.setdefault(reason, 0)
                reasons[reason] += 1
                log.warning("Zombies: could not remove %s from %s: %s", user_id, chat_id, reason)
                if failed >= 3 and removed == 0:
                    break  # the same refusal every time: stop instead of hammering Telegram
            await asyncio.sleep(PAUSE)
    except Exception:
        log.exception("Zombie sweep in %s failed", chat_id)
    finally:
        _running.discard(chat_id)

    if found == 0:
        text = tr("zombies.no_deleted_accounts_found")
    else:
        text = tr("zombies.removed_deleted_account", removed=removed, noun=plural_tag("zombies.noun", removed))
        if failed:
            text += tr("zombies.i_couldn_t_remove", failed=failed) + "; ".join(esc(reason) for reason in reasons)
    try:
        await progress.edit_text(text)
    except TelegramAPIError:
        await reply(message, text, quote=False)


@router.message(cmd("zombies"))
async def cmd_zombies(message: Message, role: Role | None = None) -> None:
    if await moderation.begin_chat_command(message, role) is None:
        return
    chat_id = message.chat.id
    if chat_id in _running:
        await reply(message, tr("zombies.i_m_already_checking_this_chat"))
        return

    user_ids = (await chats.users_in(chat_id))[:MAX_CHECKED]
    if not user_ids and not mtproto.configured():
        await reply(message, tr("zombies.i_haven_t_seen_anyone_in"))
        return

    _running.add(chat_id)
    progress = await reply(message, tr("zombies.looking_for_deleted_accounts_this_can"))
    task = asyncio.get_running_loop().create_task(_sweep(message, progress, user_ids))
    _tasks.add(task)
    task.add_done_callback(_tasks.discard)
