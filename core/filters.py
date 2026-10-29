"""Reusable filters."""

from aiogram.filters import BaseFilter, Command
from aiogram.types import TelegramObject

from config import settings
from core.roles import Role


def cmd(*names: str) -> Command:
    """
    Command filter that honours the configured prefixes (default: / and !).

        @router.message(cmd("ping"))

    Commands are case-insensitive: /Ban and /joinBlacklists work like /ban and /joinblacklists.

    Note: in groups Telegram only delivers "!" commands to bots that are
    admins or have privacy mode disabled in BotFather.
    """
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
