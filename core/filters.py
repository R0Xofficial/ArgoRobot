"""Reusable filters."""

from aiogram.filters import BaseFilter
from aiogram.types import TelegramObject

from core.roles import Role


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
