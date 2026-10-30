"""
Backups of one chat: what to save, how it looks in the file, and how it is put back.

A backup is a JSON file:

    {"argo_backup": 1, "created_at": "...", "chat": {"id": ..., "title": ...},
     "items": {"notes": [...], "approves": [...], "settings": {...}}}

An item is one thing a chat has: its notes, its filters, its approved users... A file may hold all
of them or just one, and /import takes whatever is in it. Items are described by tables below
(the columns, their types and what is acceptable), so a file from outside is never trusted: every
value is converted and checked, and a row that does not pass is skipped and counted.

Nothing here talks to Telegram. Caches of the modules are dropped after an import.
"""

import json
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Callable

from core import blacklist, chatsettings, database, filterstore, flood, joinblacklist, locks, markup, notestore

FORMAT_VERSION = 1
MAX_FILE_BYTES = 20 * 1024 * 1024
MAX_TEXT = 8000
MAX_SECONDS = 366 * 86400


class BackupError(ValueError):
    """The file can't be used; the message says why."""


# --- What a chat has ---------------------------------------------------------------------------

# Column types: i integer, s text, b yes/no, t time. A "?" after it means the value may be empty.
@dataclass
class Table:
    name: str
    columns: dict[str, str]
    key: tuple[str, ...] = ()      # the columns that make a row the same row (besides the chat)
    single: bool = False           # one row per chat, not a list
    choices: dict[str, tuple] = field(default_factory=dict)
    ranges: dict[str, tuple[int, int]] = field(default_factory=dict)
    check: Callable[[dict[str, Any]], None] | None = None  # raises ValueError for a row to skip
    limit: int = 1000


@dataclass
class Item:
    key: str
    label: str
    tables: list[Table]
    aliases: tuple[str, ...] = ()


MEDIA = ("photo", "video", "animation", "document", "audio", "voice", "sticker", "video_note")


def _content(row: dict[str, Any]) -> None:
    try:
        markup.check(row.get("content") or "")
    except markup.MarkupError as exc:
        raise ValueError(str(exc)) from None


def _note(row: dict[str, Any]) -> None:
    row["name"] = notestore.check_name(row["name"])
    _content(row)


def _filter(row: dict[str, Any]) -> None:
    filterstore._compile(row["pattern"])
    _content(row)


def _blacklisted(row: dict[str, Any]) -> None:
    blacklist.build(row["pattern"], row.get("action"), row.get("action_seconds") or 0, row.get("reason"))


def _greeting(row: dict[str, Any]) -> None:
    for kind in ("welcome", "goodbye"):
        if row.get(f"{kind}_content"):
            try:
                markup.check(row[f"{kind}_content"])
            except markup.MarkupError as exc:
                raise ValueError(str(exc)) from None


ITEMS: list[Item] = [
    Item("settings", "Chat settings and the warn limit", [
        Table("chat_settings", {"staff_help": "b", "gban_enforce": "b", "admin_errors": "b", "anon_admin": "b", "clean_commands": "b", "reports": "b"}, single=True),
        Table("warn_settings", {"warn_limit": "i"}, single=True, ranges={"warn_limit": (1, 10)}),
    ], ("chat", "chatsettings")),
    Item("antiflood", "Antiflood settings", [
        Table("flood_settings", {"flood_limit": "i", "timer_count": "i", "timer_seconds": "i", "mode": "s", "mode_seconds": "i", "clear_flood": "b", "antichannel": "b"},
              single=True, choices={"mode": ("ban", "kick", "mute", "tban", "tmute")},
              ranges={"flood_limit": (0, 100), "timer_count": (0, 100), "timer_seconds": (0, 86400), "mode_seconds": (0, MAX_SECONDS)}),
    ], ("flood",)),
    Item("locks", "Locks, the allowlist and warning on locks", [
        Table("locks", {"item": "s"}, key=("item",), choices={"item": tuple(locks.LOCKS)}),
        Table("lock_settings", {"warn": "b"}, single=True),
        Table("lock_allowlist", {"entry": "s"}, key=("entry",), limit=500),
    ], ("lock",)),
    Item("notes", "Notes", [
        Table("notes", {"name": "s", "content": "s", "media_type": "s?", "media_file_id": "s?", "created_by": "i?", "created_at": "t?"},
              key=("name",), choices={"media_type": MEDIA}, check=_note, limit=notestore.MAX_NOTES),
    ]),
    Item("filters", "Filters", [
        Table("filters", {"pattern": "s", "content": "s", "media_type": "s?", "media_file_id": "s?", "created_by": "i?"},
              key=("pattern",), choices={"media_type": MEDIA}, check=_filter, limit=500),
    ]),
    Item("blacklist", "The blacklist and its settings", [
        Table("blacklist", {"pattern": "s", "action": "s?", "action_seconds": "i", "reason": "s?", "added_by": "i?"},
              key=("pattern",), choices={"action": blacklist.ACTIONS}, ranges={"action_seconds": (0, MAX_SECONDS)}, check=_blacklisted, limit=blacklist.MAX_TRIGGERS),
        Table("blacklist_settings", {"mode": "s", "mode_seconds": "i", "delete_matches": "b"},
              single=True, choices={"mode": ("off",) + blacklist.ACTIONS}, ranges={"mode_seconds": (0, MAX_SECONDS)}),
    ], ("blacklists",)),
    Item("joinblacklist", "The join blacklists", [
        Table("join_blacklist", {"kind": "s", "pattern": "s"}, key=("kind", "pattern"),
              choices={"kind": ("name", "username", "bio", "channel")}, limit=1000),
    ], ("joinblacklists",)),
    Item("greetings", "Welcome and goodbye messages", [
        Table("greetings", {"welcome_on": "b", "welcome_content": "s?", "welcome_media_type": "s?", "welcome_media_file_id": "s?",
                            "goodbye_on": "b", "goodbye_content": "s?", "goodbye_media_type": "s?", "goodbye_media_file_id": "s?",
                            "welcome_mute": "b", "clean_welcome": "b", "clean_service": "b"},
              single=True, choices={"welcome_media_type": MEDIA, "goodbye_media_type": MEDIA}, check=_greeting),
    ], ("welcome", "goodbye", "welcomes")),
    Item("rules", "The rules", [
        Table("rules", {"content": "s", "media_type": "s?", "media_file_id": "s?"}, single=True, choices={"media_type": MEDIA}, check=_content),
    ]),
    Item("warns", "Warns", [
        Table("warns", {"user_id": "i", "user_name": "s?", "reason": "s?", "warned_by": "i?", "warned_by_name": "s?", "warned_by_staff": "b", "created_at": "t"},
              key=("user_id", "created_at"), limit=50_000),
    ], ("warn",)),
    Item("approves", "Approved users", [
        Table("approvals", {"user_id": "i", "name": "s?", "approved_by": "i?", "approved_by_name": "s?", "approved_at": "t?"},
              key=("user_id",), limit=50_000),
    ], ("approved", "approvals", "approve")),
]
BY_NAME = {name: item for item in ITEMS for name in (item.key, *item.aliases)}


def resolve(words: list[str]) -> list[Item]:
    """The items the words name (all of them without words). Raises BackupError for an unknown word."""
    if not words or words == ["all"]:
        return list(ITEMS)
    found: list[Item] = []
    for word in words:
        item = BY_NAME.get(word.lower())
        if item is None:
            raise BackupError(f"I don't know \"{word}\". Items: {', '.join(item.key for item in ITEMS)}.")
        if item not in found:
            found.append(item)
    return found


# --- Export --------------------------------------------------------------------------------------

def _out(value: Any) -> Any:
    if isinstance(value, datetime):
        return value.astimezone(timezone.utc).isoformat()
    return value


async def _export_table(table: Table, chat_id: int) -> Any:
    columns = ", ".join(table.columns)
    order = " ORDER BY " + ", ".join(table.key) if table.key else ""
    rows = await database.fetch_all(f"SELECT {columns} FROM {table.name} WHERE chat_id = $1{order}", chat_id)
    data = [{column: _out(row[column]) for column in table.columns} for row in rows]
    if table.single:
        return data[0] if data else None
    return data


async def export_items(chat_id: int, title: str | None, items: list[Item]) -> tuple[bytes, dict[str, int]]:
    """(file content, how many entries each item has). Items the chat has nothing for are left out."""
    body: dict[str, Any] = {}
    counts: dict[str, int] = {}
    for item in items:
        parts: dict[str, Any] = {}
        total = 0
        for table in item.tables:
            data = await _export_table(table, chat_id)
            if data in (None, []):
                continue
            parts[table.name] = data
            total += 1 if table.single else len(data)
        if parts:
            # An item with a single table is the list itself; with several, one entry per table.
            body[item.key] = next(iter(parts.values())) if len(item.tables) == 1 else parts
            counts[item.key] = total
    document = {
        "argo_backup": FORMAT_VERSION,
        "created_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "chat": {"id": chat_id, "title": title},
        "items": body,
    }
    return json.dumps(document, ensure_ascii=False, indent=1).encode(), counts


# --- Import --------------------------------------------------------------------------------------

def read(raw: bytes) -> dict[str, Any]:
    """The items of a backup file. Raises BackupError if it is not one."""
    try:
        document = json.loads(raw.decode("utf-8-sig", errors="replace"))
    except ValueError:
        raise BackupError("That is not a valid JSON file.") from None
    if not isinstance(document, dict) or "argo_backup" not in document or not isinstance(document.get("items"), dict):
        raise BackupError("That is not a backup made by /export.")
    if document["argo_backup"] != FORMAT_VERSION:
        raise BackupError("That backup is from a version I don't know.")
    return document["items"]


def _coerce(kind: str, value: Any) -> Any:
    nullable = kind.endswith("?")
    kind = kind.rstrip("?")
    if value is None or value == "":
        if nullable:
            return None
        if kind == "s" and value == "":
            return ""
        raise ValueError("a value is missing")
    if kind == "i":
        if isinstance(value, bool) or not isinstance(value, (int, str)) or (isinstance(value, str) and not value.lstrip("-").isdigit()):
            raise ValueError("not a number")
        return int(value)
    if kind == "b":
        if isinstance(value, int) and value in (0, 1):
            return bool(value)
        if not isinstance(value, bool):
            raise ValueError("not yes/no")
        return value
    if kind == "t":
        try:
            when = datetime.fromisoformat(str(value))
        except ValueError:
            raise ValueError("not a time") from None
        return when if when.tzinfo else when.replace(tzinfo=timezone.utc)
    if not isinstance(value, str):
        raise ValueError("not text")
    if len(value) > MAX_TEXT:
        raise ValueError("too long")
    return value


def _clean(table: Table, raw: Any) -> dict[str, Any]:
    """A row of a backup as values for the database. Raises ValueError for a row that can't be used."""
    if not isinstance(raw, dict):
        raise ValueError("not an entry")
    row: dict[str, Any] = {}
    for column, kind in table.columns.items():
        if column not in raw:
            if column in table.key:
                raise ValueError(f"{column} is missing")
            continue  # the database default is used
        row[column] = _coerce(kind, raw[column])
        if row[column] is None and kind == "t?":
            del row[column]  # the database fills the time in
            continue
        if column in table.choices and row[column] is not None and row[column] not in table.choices[column]:
            raise ValueError(f"{column} has a value I don't know")
        if column in table.ranges and not table.ranges[column][0] <= row[column] <= table.ranges[column][1]:
            raise ValueError(f"{column} is out of range")
    for column in table.key:
        if row.get(column) in (None, ""):
            raise ValueError(f"{column} is missing")
    if "user_id" in row and row["user_id"] <= 0:
        raise ValueError("not a user")
    if table.check is not None:
        table.check(row)
    return row


async def _insert(table: Table, chat_id: int, row: dict[str, Any], overwrite: bool) -> bool:
    """Write one row; True when the database changed."""
    columns = list(row)
    names = ", ".join(["chat_id", *columns])
    marks = ", ".join(f"${i}" for i in range(1, len(columns) + 2))
    args = [chat_id, *(row[c] for c in columns)]
    if table.single:
        if overwrite:
            updates = ", ".join(f"{c} = EXCLUDED.{c}" for c in columns)
            sql = f"INSERT INTO {table.name} ({names}) VALUES ({marks}) ON CONFLICT (chat_id) DO UPDATE SET {updates}"
        else:
            sql = f"INSERT INTO {table.name} ({names}) VALUES ({marks}) ON CONFLICT (chat_id) DO NOTHING"
        found = await database.fetch_one(sql + " RETURNING chat_id", *args)
        return found is not None
    if table.name == "warns":  # no natural key: the same warn is the same user at the same time
        sql = (
            f"INSERT INTO warns ({names}) SELECT {marks} WHERE NOT EXISTS "
            f"(SELECT 1 FROM warns WHERE chat_id = $1 AND user_id = ${columns.index('user_id') + 2} AND created_at = ${columns.index('created_at') + 2}) RETURNING id"
        )
    else:
        verb = "DO NOTHING" if not overwrite else "DO UPDATE SET " + ", ".join(f"{c} = EXCLUDED.{c}" for c in columns if c not in table.key)
        target = ", ".join(("chat_id", *table.key))
        if overwrite and all(c in table.key for c in columns):
            verb = "DO NOTHING"
        sql = f"INSERT INTO {table.name} ({names}) VALUES ({marks}) ON CONFLICT ({target}) {verb} RETURNING chat_id"
    return await database.fetch_one(sql, *args) is not None


async def _import_table(table: Table, chat_id: int, data: Any, overwrite: bool, counts: dict[str, int]) -> None:
    entries = [data] if table.single else data
    if not isinstance(entries, list) or (table.single and not isinstance(data, dict)):
        counts["skipped"] += 1
        return
    if len(entries) > table.limit:
        counts["skipped"] += len(entries) - table.limit
        entries = entries[: table.limit]
    rows = []
    for entry in entries:
        try:
            rows.append(_clean(table, entry))
        except Exception:  # a bad row only costs itself
            counts["skipped"] += 1
            continue
    if overwrite and not table.single:
        await database.execute(f"DELETE FROM {table.name} WHERE chat_id = $1", chat_id)
    for row in rows:
        counts["added" if await _insert(table, chat_id, row, overwrite) else "kept"] += 1


def _forget(chat_id: int) -> None:
    chatsettings.forget(chat_id)
    flood.forget(chat_id)
    blacklist.forget(chat_id)
    filterstore.forget(chat_id)
    joinblacklist.forget(chat_id)
    locks.forget(chat_id)


async def import_items(chat_id: int, content: dict[str, Any], wanted: list[Item], overwrite: bool) -> dict[str, dict[str, int]]:
    """
    Put the wanted items of a backup into the chat. Without overwrite what the chat has is kept
    and only what is missing is added; with it the chat's own entries of that item are replaced.
    Returns, per item, how many entries were added, kept (already there) and skipped (unusable).
    """
    result: dict[str, dict[str, int]] = {}
    for item in wanted:
        data = content.get(item.key)
        if data is None:
            continue
        counts = {"added": 0, "kept": 0, "skipped": 0}
        for table in item.tables:
            part = data if len(item.tables) == 1 else (data.get(table.name) if isinstance(data, dict) else None)
            if part is None:
                continue
            await _import_table(table, chat_id, part, overwrite, counts)
        result[item.key] = counts
    _forget(chat_id)
    return result
