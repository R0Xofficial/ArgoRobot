"""
Whether a global ban or a federation ban is about to remove a user from a chat.

Kept in core so that the things that restrict or greet newcomers (captchas, antiraid, welcomes) can
leave such a person alone: restricting someone who has just been banned would turn the ban into a
mute, and nobody wants a welcome for somebody who is being removed.
"""

from aiogram import Bot

from core import approvals, chatsettings, federations, gbans, immunity, moderation, roles, whitelist


async def spared(bot: Bot, chat_id: int, user_id: int) -> bool:
    """
    Whether federation bans leave this person alone in this chat: the bot itself, immune accounts,
    staff, people approved in the chat and its admins. The whitelist is not here: it protects from
    global bans only, and a federation ban is avoided by being approved in the chat.
    """
    return (
        user_id == bot.id
        or immunity.is_immune(user_id)
        or await roles.get_role(user_id) is not None
        or await approvals.is_approved(chat_id, user_id)
        or await moderation.is_chat_admin(bot, chat_id, user_id)
    )


async def removable(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Whether a globally banned user is to be removed from this chat right now (the chat may have /gbans off)."""
    if not await chatsettings.gban_enforce(chat_id):
        return False
    return not (await whitelist.is_protected(user_id) or await spared(bot, chat_id, user_id))


async def gbanned(bot: Bot, chat_id: int, user_id: int) -> bool:
    """The user is globally banned and this chat removes such users."""
    return await gbans.is_gbanned(user_id) and await removable(bot, chat_id, user_id)


async def fbanned(bot: Bot, chat_id: int, user_id: int) -> bool:
    """The user is banned in this chat's federation (or one it subscribes to) and nobody spares them."""
    return await federations.effective_ban(chat_id, user_id) is not None and not await spared(bot, chat_id, user_id)


async def applies(bot: Bot, chat_id: int, user_id: int) -> bool:
    """Either of the two: they are being removed from this chat."""
    return await gbanned(bot, chat_id, user_id) or await fbanned(bot, chat_id, user_id)
