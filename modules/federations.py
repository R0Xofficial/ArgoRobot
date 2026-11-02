"""
Federations: chats that share one ban list (the commands and the enforcement are in
modules/federations.py).

A federation has an owner (one federation per person), admins who can ban in it, chats
that joined it (a chat is in at most one) and a list of banned users. A federation can
subscribe to others: it then inherits their bans without copying them.

Every message of every chat is checked against the ban lists, so what that needs is kept
in memory: which federation a chat is in, which federations it subscribes to and the ids
of the banned users of each federation. A chat that is in no federation costs nothing.
"""

import uuid
from datetime import datetime, timezone
from typing import Any, Iterable

from core import database
from core.users import CACHE_LIMIT

MAX_NAME = 64
MAX_ADMINS = 50
MAX_SUBS = 5
MAX_REASON = 500

_chat_fed: dict[int, str | None] = {}
_subs: dict[str, tuple[str, ...]] = {}
_ids: dict[str, set[int]] = {}


class FederationError(ValueError):
    """Something the caller asked for can't be done; the message says why."""


def forget_chat(chat_id: int) -> None:
    _chat_fed.pop(chat_id, None)


def _forget_all() -> None:
    _chat_fed.clear()
    _subs.clear()
    _ids.clear()


# --- Federations ------------------------------------------------------------------------------

def _now() -> datetime:
    return datetime.now(timezone.utc)


def clean_name(text: str) -> str:
    name = " ".join(text.split())
    if not name:
        raise FederationError("A federation needs a name.")
    if len(name) > MAX_NAME:
        raise FederationError(f"A name can be at most {MAX_NAME} characters.")
    return name


async def get(fed_id: str) -> dict[str, Any] | None:
    return await database.fetch_one("SELECT * FROM federations WHERE fed_id = $1", fed_id)


async def owned_by(user_id: int) -> dict[str, Any] | None:
    return await database.fetch_one("SELECT * FROM federations WHERE owner_id = $1", user_id)


async def create(name: str, owner_id: int, owner_name: str | None) -> dict[str, Any]:
    name = clean_name(name)
    if await owned_by(owner_id) is not None:
        raise FederationError("You already own a federation. Everyone can own only one.")
    fed_id = str(uuid.uuid4())
    await database.execute(
        "INSERT INTO federations (fed_id, name, owner_id, owner_name, created_at) VALUES ($1, $2, $3, $4, $5)",
        fed_id, name, owner_id, owner_name, _now(),
    )
    return await get(fed_id)


async def rename(fed_id: str, name: str) -> str:
    name = clean_name(name)
    await database.execute("UPDATE federations SET name = $2 WHERE fed_id = $1", fed_id, name)
    return name


async def update(fed_id: str, **fields: Any) -> None:
    allowed = {"notify", "require_reason", "log_chat_id"}
    if not fields or set(fields) - allowed:
        raise ValueError(f"unknown federation settings: {sorted(set(fields) - allowed)}")
    names = list(fields)
    assignments = ", ".join(f"{name} = ${i + 2}" for i, name in enumerate(names))
    await database.execute(f"UPDATE federations SET {assignments} WHERE fed_id = $1", fed_id, *fields.values())


async def delete(fed_id: str) -> None:
    """Remove a federation and everything about it. Bans already made in chats stay."""
    for sql in (
        "DELETE FROM fed_admins WHERE fed_id = $1",
        "DELETE FROM fed_chats WHERE fed_id = $1",
        "DELETE FROM fed_bans WHERE fed_id = $1",
        "DELETE FROM fed_subs WHERE fed_id = $1 OR sub_id = $1",
        "DELETE FROM federations WHERE fed_id = $1",
    ):
        await database.execute(sql, fed_id)
    _forget_all()


async def transfer(fed_id: str, new_owner_id: int, new_owner_name: str | None) -> None:
    if await owned_by(new_owner_id) is not None:
        raise FederationError("That person already owns a federation.")
    await database.execute(
        "UPDATE federations SET owner_id = $2, owner_name = $3 WHERE fed_id = $1", fed_id, new_owner_id, new_owner_name
    )
    await database.execute("DELETE FROM fed_admins WHERE fed_id = $1 AND user_id = $2", fed_id, new_owner_id)


# --- Admins -----------------------------------------------------------------------------------

async def admins(fed_id: str) -> list[dict[str, Any]]:
    return await database.fetch_all("SELECT user_id, name FROM fed_admins WHERE fed_id = $1 ORDER BY name, user_id", fed_id)


async def is_admin(fed: dict[str, Any], user_id: int) -> bool:
    """Whether the user is the owner or an admin of the federation."""
    if fed["owner_id"] == user_id:
        return True
    row = await database.fetch_one("SELECT 1 AS found FROM fed_admins WHERE fed_id = $1 AND user_id = $2", fed["fed_id"], user_id)
    return row is not None


async def add_admin(fed_id: str, user_id: int, name: str | None) -> None:
    if len(await admins(fed_id)) >= MAX_ADMINS:
        raise FederationError(f"A federation can have at most {MAX_ADMINS} admins.")
    await database.execute(
        "INSERT INTO fed_admins (fed_id, user_id, name) VALUES ($1, $2, $3) "
        "ON CONFLICT (fed_id, user_id) DO UPDATE SET name = EXCLUDED.name",
        fed_id, user_id, name,
    )


async def remove_admin(fed_id: str, user_id: int) -> bool:
    row = await database.fetch_one(
        "DELETE FROM fed_admins WHERE fed_id = $1 AND user_id = $2 RETURNING user_id", fed_id, user_id
    )
    return row is not None


async def feds_of(user_id: int) -> list[dict[str, Any]]:
    """Federations the user owns or administers, owned one first."""
    return await database.fetch_all(
        "SELECT f.* FROM federations f WHERE f.owner_id = $1 "
        "OR EXISTS (SELECT 1 FROM fed_admins a WHERE a.fed_id = f.fed_id AND a.user_id = $1) "
        "ORDER BY (f.owner_id = $1) DESC, f.name",
        user_id,
    )


# --- Chats ------------------------------------------------------------------------------------

async def chat_fed_id(chat_id: int) -> str | None:
    if chat_id in _chat_fed:
        return _chat_fed[chat_id]
    row = await database.fetch_one("SELECT fed_id FROM fed_chats WHERE chat_id = $1", chat_id)
    if len(_chat_fed) >= CACHE_LIMIT:
        _chat_fed.clear()
    _chat_fed[chat_id] = row["fed_id"] if row else None
    return _chat_fed[chat_id]


async def chat_fed(chat_id: int) -> dict[str, Any] | None:
    fed_id = await chat_fed_id(chat_id)
    return await get(fed_id) if fed_id else None


async def join(chat_id: int, fed_id: str) -> None:
    await database.execute(
        "INSERT INTO fed_chats (chat_id, fed_id) VALUES ($1, $2) ON CONFLICT (chat_id) DO UPDATE SET fed_id = EXCLUDED.fed_id",
        chat_id, fed_id,
    )
    forget_chat(chat_id)


async def leave(chat_id: int) -> None:
    await database.execute("DELETE FROM fed_chats WHERE chat_id = $1", chat_id)
    forget_chat(chat_id)


async def is_quiet(chat_id: int) -> bool:
    row = await database.fetch_one("SELECT quiet FROM fed_chats WHERE chat_id = $1", chat_id)
    return bool(row and row["quiet"])


async def set_quiet(chat_id: int, quiet: bool) -> None:
    await database.execute("UPDATE fed_chats SET quiet = $2 WHERE chat_id = $1", chat_id, quiet)


async def chats_of(fed_ids: Iterable[str]) -> list[int]:
    found: list[int] = []
    for fed_id in fed_ids:
        rows = await database.fetch_all("SELECT chat_id FROM fed_chats WHERE fed_id = $1", fed_id)
        found += [row["chat_id"] for row in rows if row["chat_id"] not in found]
    return found


async def chat_count(fed_id: str) -> int:
    row = await database.fetch_one("SELECT count(*) AS total FROM fed_chats WHERE fed_id = $1", fed_id)
    return row["total"]


# --- Subscriptions ----------------------------------------------------------------------------

async def subs_of(fed_id: str) -> tuple[str, ...]:
    """The ids of the federations this one subscribes to."""
    cached = _subs.get(fed_id)
    if cached is not None:
        return cached
    rows = await database.fetch_all("SELECT sub_id FROM fed_subs WHERE fed_id = $1 ORDER BY sub_id", fed_id)
    if len(_subs) >= CACHE_LIMIT:
        _subs.clear()
    _subs[fed_id] = tuple(row["sub_id"] for row in rows)
    return _subs[fed_id]


async def subscribers_of(fed_id: str) -> list[str]:
    rows = await database.fetch_all("SELECT fed_id FROM fed_subs WHERE sub_id = $1", fed_id)
    return [row["fed_id"] for row in rows]


async def subscribe(fed_id: str, sub_id: str) -> None:
    if fed_id == sub_id:
        raise FederationError("A federation can't subscribe to itself.")
    current = await subs_of(fed_id)
    if sub_id in current:
        raise FederationError("You are already subscribed to that federation.")
    if len(current) >= MAX_SUBS:
        raise FederationError(f"A federation can subscribe to at most {MAX_SUBS} others.")
    await database.execute("INSERT INTO fed_subs (fed_id, sub_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", fed_id, sub_id)
    _subs.pop(fed_id, None)


async def unsubscribe(fed_id: str, sub_id: str) -> bool:
    row = await database.fetch_one("DELETE FROM fed_subs WHERE fed_id = $1 AND sub_id = $2 RETURNING sub_id", fed_id, sub_id)
    _subs.pop(fed_id, None)
    return row is not None


# --- Bans -------------------------------------------------------------------------------------

async def _ban_ids(fed_id: str) -> set[int]:
    cached = _ids.get(fed_id)
    if cached is None:
        rows = await database.fetch_all("SELECT user_id FROM fed_bans WHERE fed_id = $1", fed_id)
        if len(_ids) >= 200:
            _ids.clear()
        cached = _ids[fed_id] = {row["user_id"] for row in rows}
    return cached


async def get_ban(fed_id: str, user_id: int) -> dict[str, Any] | None:
    if user_id not in await _ban_ids(fed_id):
        return None
    return await database.fetch_one("SELECT * FROM fed_bans WHERE fed_id = $1 AND user_id = $2", fed_id, user_id)


async def ban(fed_id: str, user_id: int, name: str | None, reason: str | None, by_id: int, by_name: str | None) -> dict[str, Any] | None:
    """Ban a user in a federation. Returns the previous ban when there was one (its reason is replaced)."""
    old = await get_ban(fed_id, user_id)
    await database.execute(
        "INSERT INTO fed_bans (fed_id, user_id, name, reason, banned_by, banned_by_name, created_at) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7) "
        "ON CONFLICT (fed_id, user_id) DO UPDATE SET name = EXCLUDED.name, reason = EXCLUDED.reason, "
        "banned_by = EXCLUDED.banned_by, banned_by_name = EXCLUDED.banned_by_name",
        fed_id, user_id, name, (reason or "")[:MAX_REASON] or None, by_id, by_name, _now(),
    )
    (await _ban_ids(fed_id)).add(user_id)
    return old


async def unban(fed_id: str, user_id: int) -> dict[str, Any] | None:
    old = await get_ban(fed_id, user_id)
    if old is None:
        return None
    await database.execute("DELETE FROM fed_bans WHERE fed_id = $1 AND user_id = $2", fed_id, user_id)
    (await _ban_ids(fed_id)).discard(user_id)
    return old


async def bans(fed_id: str) -> list[dict[str, Any]]:
    return await database.fetch_all("SELECT * FROM fed_bans WHERE fed_id = $1 ORDER BY created_at, user_id", fed_id)


async def ban_count(fed_id: str) -> int:
    return len(await _ban_ids(fed_id))


async def import_bans(fed_id: str, rows: list[tuple[int, str | None, str | None]], overwrite: bool, by_id: int, by_name: str | None) -> int:
    """Add (user id, name, reason) rows. With overwrite the old list goes first. Returns how many were added."""
    if overwrite:
        await database.execute("DELETE FROM fed_bans WHERE fed_id = $1", fed_id)
        _ids.pop(fed_id, None)
    known = await _ban_ids(fed_id)
    added = 0
    for user_id, name, reason in rows:
        if user_id in known:
            continue
        await database.execute(
            "INSERT INTO fed_bans (fed_id, user_id, name, reason, banned_by, banned_by_name, created_at) "
            "VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (fed_id, user_id) DO NOTHING",
            fed_id, user_id, name, (reason or "")[:MAX_REASON] or None, by_id, by_name, _now(),
        )
        known.add(user_id)
        added += 1
    return added


async def banned_in(user_id: int) -> list[dict[str, Any]]:
    """The federations (with the ban) the user is directly banned in."""
    return await database.fetch_all(
        "SELECT f.fed_id, f.name, b.reason, b.created_at, b.banned_by_name FROM fed_bans b "
        "JOIN federations f ON f.fed_id = b.fed_id WHERE b.user_id = $1 ORDER BY f.name",
        user_id,
    )


async def effective_ban(chat_id: int, user_id: int) -> tuple[dict[str, Any], dict[str, Any]] | None:
    """(federation, ban) when the user is banned for this chat: in its federation or one it subscribes to."""
    fed_id = await chat_fed_id(chat_id)
    if fed_id is None:
        return None
    for source in (fed_id, *await subs_of(fed_id)):
        if user_id in await _ban_ids(source):
            ban_row = await get_ban(source, user_id)
            fed = await get(source)
            if ban_row and fed:
                return fed, ban_row
    return None


async def reaches(fed_id: str) -> list[str]:
    """The federation itself and every federation that subscribes to it: where its bans apply."""
    return [fed_id, *await subscribers_of(fed_id)]


# --- Where the bot banned ---------------------------------------------------------------------

async def record_applied(chat_id: int, user_id: int) -> None:
    await database.execute("INSERT INTO fed_applied (chat_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", chat_id, user_id)


async def applied_in(user_id: int) -> list[int]:
    rows = await database.fetch_all("SELECT chat_id FROM fed_applied WHERE user_id = $1", user_id)
    return [row["chat_id"] for row in rows]


async def forget_applied(chat_id: int, user_id: int) -> None:
    await database.execute("DELETE FROM fed_applied WHERE chat_id = $1 AND user_id = $2", chat_id, user_id)
