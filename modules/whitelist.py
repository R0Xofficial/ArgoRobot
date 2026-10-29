"""Whitelist"""

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import immunity, info_hooks, logs, whitelist
from core.filters import HasRole, cmd
from core.helpers import reply
from core.roles import Role
from core.targets import TargetError, resolve_target
from core.text import esc, user_link

ABOUT = """
The whitelist shields people from global actions: someone on it can't be globally banned, in any chat I'm in. It is different from /approve, which only works in one chat.

Only my staff can change it.
"""

HELP = [
    ("/whitelist <user>", "Protect a user from global bans.", Role.SUDO),
    ("/unwhitelist <user>", "Remove a user's protection.", Role.SUDO),
]

router = Router(name="whitelist")

# Developers and sudo users (and the owner, who ranks above both) manage the list.
MINIMUM_ROLE = Role.SUDO


@router.message(cmd("whitelist", "unwhitelist"), HasRole(MINIMUM_ROLE))
async def cmd_whitelist(message: Message, command: CommandObject) -> None:
    adding = command.command == "whitelist"
    actor = message.from_user

    try:
        target = await resolve_target(message, command, allow_bots=True)
    except TargetError as exc:
        await reply(message, esc(exc))
        return

    if target.user_id == actor.id:
        await reply(message, "You can't change your own whitelist status.")
        return

    who = user_link(target.user_id, target.name)
    if immunity.is_immune(target.user_id):
        await reply(message, f"{who} is immune to all bot actions and doesn't need to be whitelisted.")
        return

    if adding:
        if not await whitelist.add(target.user_id, actor.id, target.name, actor.full_name):
            await reply(message, f"{who} is already whitelisted.")
            return
        tag = "#WHITELIST"
        await reply(message, f"{who} has been whitelisted.")
    else:
        if not await whitelist.remove(target.user_id):
            await reply(message, f"{who} is not whitelisted.")
            return
        tag = "#UNWHITELIST"
        await reply(message, f"{who} has been removed from the whitelist.")

    await logs.role_log(message.bot, logs.audit_text(tag, target.user_id, target.name, actor))


async def _info_lines(bot, user_id: int, chat_id: int | None) -> list[str]:
    lines = []
    if immunity.is_immune(user_id):
        lines.append("<b>Immune:</b> <code>Yes</code>")
    if await whitelist.is_whitelisted(user_id):
        lines.append("<b>Whitelisted:</b> <code>Yes</code>")
    return lines


info_hooks.register("whitelist", _info_lines)
