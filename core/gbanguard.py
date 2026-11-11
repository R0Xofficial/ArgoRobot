"""
Whether a globally banned user is to be removed from a chat right now (modules/gbans.py).

Kept in core so that the things that restrict newcomers (captchas, antiraid) can leave such a
person alone: restricting someone who has just been banned would turn the ban into a mute.
"""

from aiogram import Bot

from core import approvals, chatsettings, gbans, moderation, roles, whitelist


async def applies(bot: Bot, chat_id: int, user_id: int) -> bool:
    """The user is globally banned and this chat removes such users (admins, approved and whitelisted ones excepted)."""
    if not await gbans.is_gbanned(user_id):
        return False
    return await removable(bot, chat_id, user_id)


async def removable(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Whether a globally banned user is to be removed from this chat right now."""
    if not await chatsettings.gban_enforce(chat_id):
        return False
    if await whitelist.is_protected(user_id) or await roles.get_role(user_id) is not None:
        return False
    if await approvals.is_approved(chat_id, user_id):
        return False
    return not await moderation.is_chat_admin(bot, chat_id, user_id)
