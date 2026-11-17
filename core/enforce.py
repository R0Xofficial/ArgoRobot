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
from core.i18n import tr

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
    elif mode == "tban":
        await perform(bot.ban_chat_member(chat_id, user_id, until_date=until))
    else:
        kwargs = {"until_date": until} if until else {}
        await perform(bot.restrict_chat_member(chat_id, user_id, permissions=MUTED, **kwargs))
    return outcome(mode, seconds)


def outcome(mode: str, seconds: int = 0) -> str:
    """What `mode` did, as text in the current language: 'muted' or 'banned for 2 hours'."""
    if mode in ("ban", "kick"):
        return tr("enforce.banned") if mode == "ban" else tr("enforce.kicked")
    if mode == "tban":
        return tr("enforce.banned_for", duration_text=duration_text(seconds))
    return tr("enforce.muted_for", duration_text=duration_text(seconds)) if mode in TIMED_MODES and seconds else tr("enforce.muted")
