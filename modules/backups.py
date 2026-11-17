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
from core.i18n import tr

log = logging.getLogger(__name__)

ABOUT = "backups.about"

HELP_WHO = "common.chat_owner"

HELP = [
    ("/export [items]", "backups.help.save_the_chat_s_data", "common.chat_owner"),
    ("/import [items] [overwrite]", "backups.help.reply_to_a_backup_file", "common.chat_owner"),
    ("/exportitems", "backups.help.list_the_items_a_backup", "common.chat_owner"),
]

HELP_NOTES = [
    (
        "common.items",
        "backups.note.items",
    ),
    (
        "common.examples",
        "backups.note.export_everything_export_approves_only",
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
    lines = [tr("backups.items_of_a_backup")] + [f"• <code>{item.key}</code> - {esc(item.label)}" for item in backupdata.ITEMS]
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
        await reply(message, tr("common.wait_a_little_before_exporting_again"))
        return
    data, counts = await backupdata.export_items(chat.id, chat.title, items)
    if not counts:
        await reply(message, tr("backups.there_is_nothing_to_save_yet"))
        return
    _exported[chat.id] = now

    single = items[0].key if len(items) == 1 else None
    name = f"ArgoBackup_{abs(chat.id)}" + (f"_{single}" if single else "") + ".json"
    try:
        await message.bot.send_document(
            message.from_user.id,
            BufferedInputFile(data, filename=name),
            caption=tr("backups.backup_of", v=esc(chat.title or tr("common.a_chat")), summary=_summary(counts)),
        )
    except TelegramAPIError:
        _exported.pop(chat.id, None)
        await reply(message, tr("backups.i_can_t_message_you_in"))
        return
    await reply(message, tr("backups.the_backup_was_sent_to_you", summary=_summary(counts)))


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
        await reply(message, tr("backups.reply_to_a_backup_file_or"))
        return
    if (document.file_size or 0) > backupdata.MAX_FILE_BYTES:
        await reply(message, tr("common.that_file_is_too_big_the"))
        return

    try:
        buffer = io.BytesIO()
        await message.bot.download(document, destination=buffer)
        content = backupdata.read(buffer.getvalue())
    except backupdata.BackupError as exc:
        await reply(message, esc(exc))
        return
    except TelegramAPIError:
        await reply(message, tr("common.i_couldn_t_download_that_file"))
        return

    try:
        result = await backupdata.import_items(message.chat.id, content, wanted, overwrite)
    except Exception:
        log.exception("Importing a backup failed")
        await reply(message, tr("backups.the_import_failed_halfway_run_it"))
        raise
    if not result:
        await reply(message, tr("backups.the_file_has_none_of_those"))
        return

    lines = [tr("backups.backup_restored") if not overwrite else tr("backups.backup_restored_replacing")]
    for key, counts in result.items():
        text = tr("backups.written", count=counts['added']) if overwrite else tr("backups.added", count=counts['added'])
        if counts["kept"]:
            text += tr("backups.already_there", kept=counts['kept'])
        if counts["skipped"]:
            text += tr("backups.skipped", skipped=counts['skipped'])
        lines.append(f"• {esc(backupdata.BY_NAME[key].label)}: {text}")
    missing = [item.label for item in wanted if item.key not in result]
    if missing and words:
        lines.append(tr("backups.not_in_the_file") + esc(", ".join(missing)) + ".")
    await reply(message, "\n".join(lines))
