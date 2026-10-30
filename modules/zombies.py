"""Zombies: remove deleted accounts from a chat"""

import asyncio
import logging

from aiogram import Router
from aiogram.exceptions import TelegramAPIError, TelegramRetryAfter
from aiogram.types import Message

from core import chats, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError, perform
from core.roles import Role
from core.text import esc

log = logging.getLogger(__name__)

ABOUT = """
Deleted accounts ("zombies") stay in a chat's member list and just take up room. /zombies removes them.

Telegram doesn't let a bot list every member of a chat. I check the people I have seen in this chat, those who wrote, joined or left while I was here, so it finds the zombies I know of. Run it again after a while to catch more.

They are kicked, not banned. A deleted account can't come back anyway.

For chat admins with the right to ban users, and I need that right too. Only one run per chat at a time.
"""

HELP_WHO = "admins"

HELP = [
    ("/zombies", "Find the deleted accounts among the people I have seen here and remove them."),
]

router = Router(name="zombies")

DELETED_NAME = "Deleted Account"
PAUSE = 0.06  # between Telegram calls, to stay far from the flood limits
MAX_CHECKED = 5000

_running: set[int] = set()
_tasks: set[asyncio.Task] = set()


def _is_deleted(member) -> bool:
    user = getattr(member, "user", None)
    return user is not None and (getattr(user, "first_name", None) or "") == DELETED_NAME


def _present(member) -> bool:
    status = str(getattr(member.status, "value", member.status))
    if status == "restricted":
        return getattr(member, "is_member", True) is not False
    return status == "member"


async def _sweep(message: Message, progress: Message, user_ids: list[int]) -> None:
    bot, chat_id = message.bot, message.chat.id
    found = removed = failed = 0
    try:
        for user_id in user_ids:
            try:
                member = await bot.get_chat_member(chat_id, user_id)
            except TelegramRetryAfter as exc:
                await asyncio.sleep(exc.retry_after)
                continue
            except TelegramAPIError:
                await asyncio.sleep(PAUSE)
                continue
            if _is_deleted(member) and _present(member):
                found += 1
                try:
                    await perform(bot.ban_chat_member(chat_id, user_id))
                    await perform(bot.unban_chat_member(chat_id, user_id, only_if_banned=True))
                    removed += 1
                except ModerationError:
                    failed += 1
                    if failed >= 3 and removed == 0:
                        break  # no right to ban: stop instead of hammering Telegram
            await asyncio.sleep(PAUSE)
    except Exception:
        log.exception("Zombie sweep in %s failed", chat_id)
    finally:
        _running.discard(chat_id)

    if found == 0:
        text = f"No deleted accounts among the {len(user_ids)} people I know here."
    else:
        text = f"Removed {removed} deleted account{'' if removed == 1 else 's'}."
        if failed:
            text += f" I couldn't remove {failed}: {esc(moderation.RIGHTS_MESSAGE)}"
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
        await reply(message, "I'm already checking this chat.")
        return

    user_ids = (await chats.users_in(chat_id))[:MAX_CHECKED]
    if not user_ids:
        await reply(message, "I haven't seen anyone in this chat yet.")
        return

    _running.add(chat_id)
    progress = await reply(message, f"Checking {len(user_ids)} people I know here. This can take a while.")
    task = asyncio.get_running_loop().create_task(_sweep(message, progress, user_ids))
    _tasks.add(task)
    task.add_done_callback(_tasks.discard)
