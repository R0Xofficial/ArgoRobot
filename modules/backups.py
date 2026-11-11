"""Backups: save a chat's settings and data to a file, and put them back"""

import io
import logging
import time

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import BufferedInputFile, Message

from core import backupdata, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import ModerationError
from core.text import esc

log = logging.getLogger(__name__)

ABOUT = """
Save what this chat has set up, notes, filters, blacklists, rules, welcome messages, locks, warns, approved users and settings, in a file. Put it back in another chat, or in this one after a mistake.

Everything goes by default, or name the items you want: /export notes approves. A file can hold several items; /import takes what it finds and you can pick from it the same way.

/import adds what is missing and leaves what the chat already has alone. Add overwrite to replace the chat's own entries of those items with the file's.

The file is sent to you in private, so start a chat with me first. Only the owner of the chat can use these commands. Anything in a file that doesn't look right is skipped, and I tell you how many.
"""

HELP_WHO = "chat owner"

HELP = [
    ("/export [items]", "Save the chat's data to a file, sent to you in private. Without items everything is saved.", "chat owner"),
    ("/import [items] [overwrite]", "Reply to a backup file (or send it with the command) to restore it. Without overwrite only what is missing is added.", "chat owner"),
    ("/exportitems", "List the items a backup can hold.", "chat owner"),
]

HELP_NOTES = [
    (
        "Items",
        "\n".join(f"{item.key} - {item.label}" for item in backupdata.ITEMS),
    ),
    (
        "Examples",
        """
        /export - everything.
        /export approves - only the approved users.
        /export notes filters - notes and filters.
        /import - restore everything in the file, keeping what is here.
        /import notes overwrite - replace the notes with the ones in the file.
        """,
    ),
]

router = Router(name="backups")

EXPORT_COOLDOWN = 20.0
_exported: dict[int, float] = {}


async def _owner(message: Message) -> bool:
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return False
    return True


def _words(command: CommandObject) -> list[str]:
    return (command.args or "").lower().replace(",", " ").split()


def _summary(counts: dict[str, int]) -> str:
    return ", ".join(f"{key}: {total}" for key, total in counts.items())


@router.message(cmd("exportitems"))
async def cmd_exportitems(message: Message) -> None:
    if not await _owner(message):
        return
    lines = ["<b>Items of a backup</b>"] + [f"• <code>{item.key}</code> - {esc(item.label)}" for item in backupdata.ITEMS]
    await reply(message, "\n".join(lines))


@router.message(cmd("export"))
async def cmd_export(message: Message, command: CommandObject) -> None:
    if not await _owner(message):
        return
    try:
        items = backupdata.resolve(_words(command))
    except backupdata.BackupError as exc:
        await reply(message, esc(exc))
        return

    chat = message.chat
    now = time.monotonic()
    if now - _exported.get(chat.id, -EXPORT_COOLDOWN) < EXPORT_COOLDOWN:
        await reply(message, "Wait a little before exporting again.")
        return
    data, counts = await backupdata.export_items(chat.id, chat.title, items)
    if not counts:
        await reply(message, "There is nothing to save yet.")
        return
    _exported[chat.id] = now

    single = items[0].key if len(items) == 1 else None
    name = f"ArgoBackup_{abs(chat.id)}" + (f"_{single}" if single else "") + ".json"
    try:
        await message.bot.send_document(
            message.from_user.id,
            BufferedInputFile(data, filename=name),
            caption=f"Backup of {esc(chat.title or 'a chat')}: {_summary(counts)}.",
        )
    except TelegramAPIError:
        _exported.pop(chat.id, None)
        await reply(message, "I can't message you in private. Start a chat with me, then send /export again.")
        return
    await reply(message, f"The backup was sent to you in private ({_summary(counts)}).")


@router.message(cmd("import"))
async def cmd_import(message: Message, command: CommandObject) -> None:
    if not await _owner(message):
        return
    words = _words(command)
    overwrite = "overwrite" in words
    words = [word for word in words if word not in ("overwrite", "keep")]
    try:
        wanted = backupdata.resolve(words)
    except backupdata.BackupError as exc:
        await reply(message, esc(exc))
        return

    document = message.document or (message.reply_to_message.document if message.reply_to_message else None)
    if document is None:
        await reply(message, "Reply to a backup file, or send the file with /import as its caption.")
        return
    if (document.file_size or 0) > backupdata.MAX_FILE_BYTES:
        await reply(message, "That file is too big (the limit is 20 MB).")
        return

    try:
        buffer = io.BytesIO()
        await message.bot.download(document, destination=buffer)
        content = backupdata.read(buffer.getvalue())
    except backupdata.BackupError as exc:
        await reply(message, esc(exc))
        return
    except TelegramAPIError:
        await reply(message, "I couldn't download that file.")
        return

    try:
        result = await backupdata.import_items(message.chat.id, content, wanted, overwrite)
    except Exception:
        log.exception("Importing a backup failed")
        await reply(message, "The import failed halfway. Run it again; nothing is imported twice.")
        raise
    if not result:
        await reply(message, "The file has none of those items.")
        return

    lines = ["<b>Backup restored</b>" if not overwrite else "<b>Backup restored</b> (replacing)"]
    for key, counts in result.items():
        text = f"{counts['added']} {'written' if overwrite else 'added'}"
        if counts["kept"]:
            text += f", {counts['kept']} already there"
        if counts["skipped"]:
            text += f", {counts['skipped']} skipped"
        lines.append(f"• {esc(backupdata.BY_NAME[key].label)}: {text}")
    missing = [item.label for item in wanted if item.key not in result]
    if missing and words:
        lines.append("Not in the file: " + esc(", ".join(missing)) + ".")
    await reply(message, "\n".join(lines))
