"""
Ban lists as files: the formats of /fedexport, /fedimport, /gexport and /gimport.

csv      user_id,name,reason,banned_by,banned_at
minicsv  user_id,reason
json     a list of {"user_id": ..., "name": ..., "reason": ...}; a file with one such object
         per line (JSON lines, as other bots export it) is read too
human    one line per user, like: Name [123456] - reason

Rows are dicts with user_id, name, reason, banned_by_name and created_at.
Nothing here talks to Telegram or the database.
"""

import csv
import io
import json
import re
from typing import Any

FORMATS = ("csv", "minicsv", "json", "human")
MAX_IMPORT_BYTES = 20 * 1024 * 1024  # what a bot can download
MAX_IMPORT_ROWS = 200_000


def stamp(row: dict[str, Any]) -> str:
    when = row.get("created_at")
    return when.strftime("%Y-%m-%d %H:%M:%S") if hasattr(when, "strftime") else str(when or "")


def render_export(rows: list[dict[str, Any]], fmt: str) -> tuple[bytes, str]:
    """(file content, extension) of a ban list."""
    if fmt == "json":
        data = [
            {"user_id": r["user_id"], "name": r["name"], "reason": r["reason"], "banned_by": r["banned_by_name"], "banned_at": stamp(r)}
            for r in rows
        ]
        return json.dumps(data, ensure_ascii=False, indent=1).encode(), "json"
    if fmt == "human":
        lines = [f"{r['name'] or 'Unknown'} [{r['user_id']}]" + (f" - {r['reason']}" if r["reason"] else "") for r in rows]
        return "\n".join(lines).encode(), "txt"
    out = io.StringIO()
    writer = csv.writer(out, lineterminator="\n")
    if fmt == "minicsv":
        writer.writerow(["user_id", "reason"])
        for r in rows:
            writer.writerow([r["user_id"], r["reason"] or ""])
    else:
        writer.writerow(["user_id", "name", "reason", "banned_by", "banned_at"])
        for r in rows:
            writer.writerow([r["user_id"], r["name"] or "", r["reason"] or "", r["banned_by_name"] or "", stamp(r)])
    return out.getvalue().encode(), "csv"


def _json_lines(text: str) -> list:
    """The objects of a file with one JSON value per line; lines that are not JSON are skipped."""
    found = []
    for line in text.splitlines():
        line = line.strip().rstrip(",")
        if not line or line in ("[", "]"):
            continue
        try:
            found.append(json.loads(line))
        except ValueError:
            continue
    if not found:
        raise ValueError("That is not valid JSON.")
    return found


def _id_of(text: str) -> int | None:
    text = text.strip()
    return int(text) if re.fullmatch(r"-?\d{1,15}", text) and int(text) > 0 else None


def parse_import(text: str, fmt: str | None) -> list[tuple[int, str | None, str | None]]:
    """(user id, name, reason) rows from a file's text. Raises ValueError with a sentence."""
    text = text.lstrip("﻿").strip()
    if not text:
        raise ValueError("The file is empty.")
    if fmt is None:
        if text[0] in "[{":
            fmt = "json"
        elif re.match(r"^(user_id|id)\b", text, re.IGNORECASE) or re.match(r"^\d+\s*(,|$)", text.splitlines()[0]):
            fmt = "csv"
        else:
            fmt = "human"
    rows: list[tuple[int, str | None, str | None]] = []
    if fmt == "json":
        try:
            data = json.loads(text)
        except ValueError:
            data = _json_lines(text)  # one object per line
        if isinstance(data, dict):
            data = data.get("bans", data.get("users", [data]))
        if not isinstance(data, list):
            raise ValueError("I expected a list of bans.")
        for entry in data:
            if isinstance(entry, dict):
                user_id = _id_of(str(entry.get("user_id", entry.get("id", ""))))
                name, reason = entry.get("name"), entry.get("reason")
            else:
                user_id, name, reason = _id_of(str(entry)), None, None
            if user_id:
                rows.append((user_id, str(name) if name else None, str(reason) if reason else None))
    elif fmt in ("csv", "minicsv"):
        table = list(csv.reader(io.StringIO(text)))
        columns = {"user_id": 0, "name": 1, "reason": 2} if fmt == "csv" else {"user_id": 0, "reason": 1}
        if table and _id_of(table[0][0] if table[0] else "") is None:
            header = [cell.strip().lower() for cell in table.pop(0)]
            columns = {key: header.index(key) for key in ("user_id", "name", "reason") if key in header} or columns
            if "user_id" not in columns and "id" in header:
                columns["user_id"] = header.index("id")
        for row in table:
            cell = lambda key: (row[columns[key]].strip() if key in columns and columns[key] < len(row) else "")
            user_id = _id_of(cell("user_id"))
            if user_id:
                rows.append((user_id, cell("name") or None, cell("reason") or None))
    else:
        for line in text.splitlines():
            found = re.search(r"\[(\d{1,15})\]", line) or re.match(r"^\s*(\d{1,15})\b", line)
            if not found:
                continue
            before, after = line[: found.start()], line[found.end():]
            reason = re.sub(r"^\s*[-:–—,]\s*", "", after).strip() or None
            name = before.strip() or None
            rows.append((int(found.group(1)), name, reason))
    if not rows:
        raise ValueError("I found no user IDs in that file.")
    if len(rows) > MAX_IMPORT_ROWS:
        raise ValueError(f"That is too many bans: at most {MAX_IMPORT_ROWS} at once.")
    return rows
