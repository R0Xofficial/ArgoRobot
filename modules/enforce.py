"""
Punishments applied automatically by modules (antiflood, blacklists, ...).

One place decides what "mute", "tban" and the rest mean, so every module words and
performs them the same way. Raises ModerationError when Telegram refuses (usually: the
bot lacks the right to restrict users).
"""

from datetime import datetime, timedelta, timezone

from aiogram.types import ChatPermissions

from core.moderation import perform
from core.text import duration_text

MUTED = ChatPermissions(can_send_messages=False)

MODES = ("ban", "kick", "mute", "tban", "tmute")
TIMED_MODES = ("tban", "tmute")


async def punish(bot, chat_id: int, user_id: int, mode: str, seconds: int = 0) -> str:
    """Apply `mode` to a user. Returns what was done, e.g. 'muted' or 'banned for 2 hours'."""
    until = None
    if mode in TIMED_MODES:
        until = datetime.now(timezone.utc) + timedelta(seconds=seconds)

    if mode in ("ban", "kick"):
        await perform(bot.ban_chat_member(chat_id, user_id))
        if mode == "kick":
            await perform(bot.unban_chat_member(chat_id, user_id, only_if_banned=True))
        return "banned" if mode == "ban" else "kicked"
    if mode == "tban":
        await perform(bot.ban_chat_member(chat_id, user_id, until_date=until))
        return f"banned for {duration_text(seconds)}"
    kwargs = {"until_date": until} if until else {}
    await perform(bot.restrict_chat_member(chat_id, user_id, permissions=MUTED, **kwargs))
    return f"muted for {duration_text(seconds)}" if until else "muted"
