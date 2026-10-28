"""
Core handlers. Always active, not part of the module system.

Replies use message.answer(), which automatically stays in the same topic
(message_thread_id) when the command was sent inside a forum topic.
"""

import html

from aiogram import Router
from aiogram.filters import Command, CommandObject, CommandStart
from aiogram.types import Message

from core import registry
from core.filters import HasRole
from core.roles import Role

router = Router(name="core")


@router.message(CommandStart())
async def cmd_start(message: Message) -> None:
    await message.answer(
        "Argo online. Keeping this group functional, one questionable decision at a time."
    )


@router.message(Command("modules"), HasRole(Role.DEVELOPER))
async def cmd_modules(message: Message) -> None:
    modules = registry.all_modules()
    if not modules:
        await message.answer("No modules loaded.")
        return

    lines = ["<b>Modules</b>"]
    for info in modules:
        status = "enabled" if info.enabled else "disabled"
        line = f"<code>{html.escape(info.name)}</code> - {status}"
        if info.description:
            line += f" ({html.escape(info.description)})"
        lines.append(line)

    await message.answer("\n".join(lines))


@router.message(Command("module"), HasRole(Role.DEVELOPER))
async def cmd_module(message: Message, command: CommandObject) -> None:
    args = (command.args or "").split()
    action = args[0].lower() if args else ""

    if len(args) != 2 or action not in ("enable", "disable"):
        await message.answer("Usage: /module &lt;enable|disable&gt; &lt;name&gt;")
        return

    name = args[1]
    if not await registry.set_enabled(name, action == "enable"):
        await message.answer(f"Module <code>{html.escape(name)}</code> not found.")
        return

    past = "enabled" if action == "enable" else "disabled"
    await message.answer(f"Module <code>{html.escape(name.lower())}</code> {past}.")
