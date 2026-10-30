"""Reusable filters."""

import sys

from aiogram.filters import BaseFilter, Command
from aiogram.types import TelegramObject

from config import settings
from core import disabled
from core.roles import Role


def _calling_module() -> str:
    """Name of the module (modules/<name>.py) whose code called cmd()."""
    parts = sys._getframe(2).f_globals.get("__name__", "").split(".")
    return parts[1] if len(parts) > 1 and parts[0] == "modules" else ""


def cmd(*names: str) -> Command:
    """
    Command filter that honours the configured prefixes (default: / and !).

        @router.message(cmd("ping"))

    Commands are case-insensitive: /Ban and /joinBlacklists work like /ban and /joinblacklists.

    Note: in groups Telegram only delivers "!" commands to bots that are
    admins or have privacy mode disabled in BotFather.
    """
    disabled.record(names, _calling_module())  # so chats can switch the command off (core/disabled.py)
    return Command(*names, prefix=settings.COMMAND_PREFIXES, ignore_case=True)


class HasRole(BaseFilter):
    """
    Passes when the user's role is at least `minimum`.

        @router.message(Command("ban"), HasRole(Role.SUDO))

    Users without the role simply do not match, so the command is silently
    ignored for them.
    """

    def __init__(self, minimum: Role) -> None:
        self.minimum = minimum

    async def __call__(self, event: TelegramObject, role: Role | None = None) -> bool:
        return role is not None and role >= self.minimum
