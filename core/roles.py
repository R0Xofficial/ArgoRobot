"""
Bot-wide roles.

    SUPPORT < SUDO < DEVELOPER < OWNER

The OWNER is defined only by OWNER_ID in the configuration. It is never stored
in the database, so no command can assign, change or remove it. Permission-wise
OWNER is equal to DEVELOPER; the difference is that it cannot be demoted.

Staff roles (everything except OWNER) live in the database and are mirrored in
memory, so looking up a role on every update costs no database query.

Who may grant or revoke which role is defined in MANAGED_BY.
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


# Minimum role needed to grant or revoke each role.
MANAGED_BY: dict[Role, Role] = {
    Role.DEVELOPER: Role.OWNER,
    Role.SUDO: Role.DEVELOPER,
    Role.SUPPORT: Role.DEVELOPER,
}

# Role log tags: (granted, revoked).
LOG_TAGS: dict[Role, tuple[str, str]] = {
    Role.DEVELOPER: ("#DEVELOPER", "#UNDEVELOPER"),
    Role.SUDO: ("#SUDO", "#UNSUDO"),
    Role.SUPPORT: ("#SUPPORT", "#UNSUPPORT"),
}


def label(role: Role, plural: bool = False) -> str:
    """Human readable name: 'Sudo', 'Developers'."""
    return role.name.capitalize() + ("s" if plural else "")


def can_manage(actor: Role | None, target: Role) -> bool:
    """Whether `actor` may grant or revoke `target`."""
    needed = MANAGED_BY.get(target)
    return needed is not None and actor is not None and actor >= needed


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


async def set_role(
    user_id: int,
    role: Role,
    added_by: int | None = None,
    name: str | None = None,
    added_by_name: str | None = None,
) -> None:
    if role is Role.OWNER:
        raise ValueError("The OWNER role is set in the configuration and cannot be assigned.")
    if user_id == settings.OWNER_ID:
        raise ValueError("The owner's role cannot be changed.")

    await database.execute(
        "INSERT INTO roles (user_id, role, added_by, name, added_by_name) "
        "VALUES ($1, $2, $3, $4, $5) "
        "ON CONFLICT (user_id) DO UPDATE SET role = EXCLUDED.role, "
        "added_by = EXCLUDED.added_by, name = EXCLUDED.name, "
        "added_by_name = EXCLUDED.added_by_name, added_at = now()",
        user_id,
        int(role),
        added_by,
        name,
        added_by_name,
    )
    await _load()


async def remove_role(user_id: int) -> None:
    if user_id == settings.OWNER_ID:
        raise ValueError("The owner cannot be demoted.")

    await database.execute("DELETE FROM roles WHERE user_id = $1", user_id)
    await _load()


async def list_role(role: Role) -> list[dict]:
    """Staff members holding `role`, oldest first.

    Each item: user_id, name, added_at (datetime, UTC), added_by, added_by_name.
    """
    return await database.fetch_all(
        "SELECT user_id, name, added_at, added_by, added_by_name "
        "FROM roles WHERE role = $1 ORDER BY added_at, user_id",
        int(role),
    )
