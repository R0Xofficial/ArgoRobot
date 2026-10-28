"""
Bot-wide roles.

    SUPPORT < SUDO < DEVELOPER < OWNER

The OWNER is defined only by OWNER_ID in the configuration. It is never stored
in the database, so no command can assign, change or remove it. Permission-wise
OWNER is equal to DEVELOPER; the difference is that it cannot be demoted.

Staff roles (everything except OWNER) live in the database and are mirrored in
memory, so looking up a role on every update costs no database query.
"""

import logging
from enum import IntEnum

from config import settings
from core import database

log = logging.getLogger(__name__)


class Role(IntEnum):
    SUPPORT = 1
    SUDO = 2
    DEVELOPER = 3
    OWNER = 4


_staff: dict[int, Role] | None = None


async def _load() -> dict[int, Role]:
    global _staff
    rows = await database.fetch_all("SELECT user_id, role FROM roles")
    _staff = {row["user_id"]: Role(row["role"]) for row in rows}
    log.info("Loaded %d staff role(s)", len(_staff))
    return _staff


async def get_role(user_id: int) -> Role | None:
    """Return the user's role, or None for regular users."""
    if user_id == settings.OWNER_ID:
        return Role.OWNER
    staff = _staff if _staff is not None else await _load()
    return staff.get(user_id)


async def set_role(user_id: int, role: Role, added_by: int | None = None) -> None:
    if role is Role.OWNER:
        raise ValueError("The OWNER role is set in the configuration and cannot be assigned.")
    if user_id == settings.OWNER_ID:
        raise ValueError("The owner's role cannot be changed.")

    await database.execute(
        "INSERT INTO roles (user_id, role, added_by) VALUES (?, ?, ?) "
        "ON CONFLICT(user_id) DO UPDATE SET role = excluded.role, added_by = excluded.added_by",
        (user_id, int(role), added_by),
    )
    await _load()


async def remove_role(user_id: int) -> None:
    if user_id == settings.OWNER_ID:
        raise ValueError("The owner cannot be demoted.")

    await database.execute("DELETE FROM roles WHERE user_id = ?", (user_id,))
    await _load()
