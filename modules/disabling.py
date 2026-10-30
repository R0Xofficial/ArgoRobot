"""Switching commands off"""

from contextlib import suppress

from aiogram import Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import connection, disabled, moderation, registry
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES, ModerationError
from core.roles import Role
from core.text import esc

ABOUT = """
Chats can switch off what they don't need, in three ways.

/disable switches off single commands for members. /restrictmodule does the same for every command of a module. Admins can still use what is switched off for members, so nobody is locked out of their own tools. Aliases go with the command.

/disablemodule turns a whole module off in the chat completely: its commands stop for everybody, admins included, and so does everything it does by itself (filters, locks, flood control, welcome messages ...). Everything can go except this page: even /help and /connect. Turning connections off ends the ones that exist. /disablemodule all turns off everything but this page; a chat that only wants global bans sends it and then /enablemodule gbans. /enablemodule brings modules back. These two are for the chat's owner.

Only the commands on this page can never be switched off, so a chat can always undo what it did.
"""

HELP = [
    ("/disable <command>", "Switch a command off for members of this chat, for example /disable slap. Admins can still use it. Needs the right to change chat info.", "admins"),
    ("/enable <command>", "Switch a command back on. Needs the right to change chat info.", "admins"),
    ("/restrictmodule <module>", "Switch off every command of a module for members, for example /restrictmodule fun. Admins can still use them. Use it without a name to see the modules. Needs the right to change chat info.", "admins"),
    ("/unrestrictmodule <module>", "Switch a module's commands back on for members. Needs the right to change chat info.", "admins"),
    ("/disablemodule <module|all>", "Turn a module off in this chat completely: its commands and its automatic actions stop, for everybody, admins included. all turns off every module (this page excepted). Use it without a name to see the modules.", "chat owner"),
    ("/enablemodule <module|all>", "Turn a module back on in this chat. all turns every module back on.", "chat owner"),
    ("/disabled", "Show what is switched off in this chat.", "everyone"),
]

router = Router(name="disabling")

RIGHT = "can_change_info"


def _slash(name: str) -> str:
    return f"/{name}"


async def _begin(message: Message, role: Role | None, right: str = RIGHT) -> bool:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return False
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=right) is not None


def _word(command: CommandObject) -> str:
    """The first word after the command, without a leading / or !, in lower case."""
    words = (command.args or "").split()
    return words[0].lstrip("/!").lower() if words else ""


def _switchable() -> list[str]:
    """Modules a chat may turn off."""
    return sorted(m.name for m in registry.all_modules() if m.name not in disabled.ALWAYS_ON)


async def _switch_command(message: Message, command: CommandObject, role: Role | None, *, off: bool) -> None:
    name = "disable" if off else "enable"
    if not await _begin(message, role):
        return
    word = _word(command)
    if not word:
        await reply(message, f"Use /{name} &lt;command&gt;, for example /{name} slap.")
        return
    entry = disabled.lookup(word)
    if entry is None:
        await reply(message, f"I don't have a command called <code>{_slash(esc(word))}</code>.")
        return
    primary = entry[0]
    if primary in disabled.PROTECTED:
        await reply(message, f"{_slash(primary)} can't be switched off.")
        return
    chat_id = message.chat.id
    changed = await (disabled.add if off else disabled.remove)(chat_id, primary)
    others = disabled.aliases_of(primary)
    extra = f" Its aliases ({', '.join(_slash(a) for a in others)}) follow." if others else ""
    if off:
        if changed:
            await reply(message, f"{_slash(primary)} is now switched off for members. Admins can still use it.{extra}")
        else:
            await reply(message, f"{_slash(primary)} is already switched off.")
    else:
        if changed:
            await reply(message, f"{_slash(primary)} is switched on again.{extra}")
        else:
            await reply(message, f"{_slash(primary)} isn't switched off.")


@router.message(cmd("disable"))
async def cmd_disable(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_command(message, command, role, off=True)


@router.message(cmd("enable"))
async def cmd_enable(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_command(message, command, role, off=False)


def _modules() -> list[str]:
    """Modules with at least one command that can be switched off for members."""
    return sorted(m.name for m in registry.all_modules() if disabled.commands_of(m.name))


async def _switch_members(message: Message, command: CommandObject, role: Role | None, *, off: bool) -> None:
    name = "restrictmodule" if off else "unrestrictmodule"
    if not await _begin(message, role):
        return
    word = _word(command)
    listing = ", ".join(f"<code>{m}</code>" for m in _modules())
    if not word:
        await reply(message, f"Use /{name} &lt;module&gt;.\nModules: {listing}")
        return
    if word not in _modules():
        await reply(message, f"I don't have a module called <code>{esc(word)}</code>.\nModules: {listing}")
        return
    changed = await (disabled.add if off else disabled.remove)(message.chat.id, disabled.module_key(word))
    if off:
        text = "now switched off for members. Admins can still use them." if changed else "already switched off for members."
    else:
        text = "switched on again for members." if changed else "not switched off for members."
    await reply(message, f"The commands of <b>{word}</b> are {text}")


@router.message(cmd("restrictmodule"))
async def cmd_restrictmodule(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_members(message, command, role, off=True)


@router.message(cmd("unrestrictmodule"))
async def cmd_unrestrictmodule(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_members(message, command, role, off=False)


async def _end_connections(message: Message) -> str:
    """Connections to a chat that turned them off end; the people on them are told. Returns a sentence for the answer."""
    people = await connection.drop_chat(message.chat.id)
    title = esc(message.chat.title or str(message.chat.id))
    for user_id in people:
        with suppress(TelegramAPIError):
            await message.bot.send_message(
                chat_id=user_id,
                text=f"Your connection to <b>{title}</b> has ended: connections were switched off there.",
            )
    return f"\n{len(people)} connection{'s' if len(people) != 1 else ''} to this chat ended." if people else ""


async def _switch_module(message: Message, command: CommandObject, *, off: bool) -> None:
    name = "disablemodule" if off else "enablemodule"
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    try:
        await moderation.require_chat_owner(message)
    except ModerationError as exc:
        await moderation.deny(message, exc)
        return
    word = _word(command)
    names = _switchable()
    listing = ", ".join(f"<code>{m}</code>" for m in names)
    if not word:
        await reply(message, f"Use /{name} &lt;module&gt; or /{name} all.\nModules: {listing}")
        return
    chat_id = message.chat.id
    change = disabled.add if off else disabled.remove

    if word == "all":
        changed = [m for m in names if await change(chat_id, disabled.off_key(m))]
        if not off:
            await reply(message, "Every module is on again." if changed else "Every module was already on.")
        elif changed:
            ended = await _end_connections(message) if "connection" in changed else ""
            await reply(message, f"{len(changed)} modules are now off in this chat.\nBring one back with /enablemodule &lt;module&gt;, for example /enablemodule gbans." + ended)
        else:
            await reply(message, "Everything is already off here.")
        return

    if word not in names:
        if registry.get(word) is not None:
            await reply(message, f"<b>{esc(word)}</b> is part of my core and can't be switched off.")
        else:
            await reply(message, f"I don't have a module called <code>{esc(word)}</code>.\nModules: {listing}")
        return
    changed = await change(chat_id, disabled.off_key(word))
    if off:
        ended = await _end_connections(message) if changed and word == "connection" else ""
        await reply(message, (f"<b>{word}</b> is now off in this chat: its commands and automatic actions stop." + ended) if changed else f"<b>{word}</b> is already off here.")
    else:
        await reply(message, f"<b>{word}</b> is on again in this chat." if changed else f"<b>{word}</b> wasn't turned off here.")


@router.message(cmd("disablemodule"))
async def cmd_disablemodule(message: Message, command: CommandObject) -> None:
    await _switch_module(message, command, off=True)


@router.message(cmd("enablemodule"))
async def cmd_enablemodule(message: Message, command: CommandObject) -> None:
    await _switch_module(message, command, off=False)


@router.message(cmd("disabled"))
async def cmd_disabled(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    found = await disabled.get(message.chat.id)
    off = sorted(k.split(":", 1)[1] for k in found if k.startswith("off:"))
    members = sorted(k.split(":", 1)[1] for k in found if k.startswith("module:"))
    commands = sorted(k for k in found if ":" not in k)
    if not off and not members and not commands:
        await reply(message, "Nothing is switched off in this chat.")
        return
    lines = []
    if commands:
        lines.append("Commands switched off for members (admins can still use them): " + ", ".join(_slash(c) for c in commands))
    if members:
        lines.append("Modules switched off for members (admins can still use them): " + ", ".join(f"<code>{m}</code>" for m in members))
    if off:
        lines.append("Modules turned off for everybody: " + ", ".join(f"<code>{m}</code>" for m in off))
    await reply(message, "\n".join(lines))
