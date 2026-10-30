"""Switching commands off"""

from aiogram import Router
from aiogram.filters import CommandObject
from aiogram.types import Message

from core import disabled, moderation, registry
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.text import esc

ABOUT = """
Chats can switch off commands they don't want members to use, one command or a whole module at a time.

Admins can still use a switched-off command, so nobody is locked out of their own tools. Members who use one are ignored. Aliases are switched off together with the command. A few commands can never be switched off: the ones on this page, /help, /start and /connect.
"""

HELP = [
    ("/disable <command>", "Switch a command off for members of this chat, for example /disable slap. Needs the right to change chat info.", "admins"),
    ("/enable <command>", "Switch a command back on. Needs the right to change chat info.", "admins"),
    ("/disablemodule <module>", "Switch off every command of a module, for example /disablemodule fun. Use it without a name to see the modules. Needs the right to change chat info.", "admins"),
    ("/enablemodule <module>", "Switch a module's commands back on. Needs the right to change chat info.", "admins"),
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


def _modules() -> list[str]:
    """Modules with at least one command that can be switched off."""
    return sorted(m.name for m in registry.all_modules() if not m.protected and disabled.commands_of(m.name))


def _module_of(word: str) -> str | None:
    return word if word in _modules() else None


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
        elif disabled.module_key(entry[1]) in await disabled.get(chat_id):
            await reply(message, f"{_slash(primary)} isn't switched off by itself: the whole module <b>{esc(entry[1])}</b> is. Use /enablemodule {esc(entry[1])}.")
        else:
            await reply(message, f"{_slash(primary)} isn't switched off.")


@router.message(cmd("disable"))
async def cmd_disable(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_command(message, command, role, off=True)


@router.message(cmd("enable"))
async def cmd_enable(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_command(message, command, role, off=False)


async def _switch_module(message: Message, command: CommandObject, role: Role | None, *, off: bool) -> None:
    name = "disablemodule" if off else "enablemodule"
    if not await _begin(message, role):
        return
    word = _word(command)
    listing = ", ".join(f"<code>{m}</code>" for m in _modules())
    if not word:
        await reply(message, f"Use /{name} &lt;module&gt;.\nModules: {listing}")
        return
    module = _module_of(word)
    if module is None:
        await reply(message, f"I don't have a module called <code>{esc(word)}</code>.\nModules: {listing}")
        return
    changed = await (disabled.add if off else disabled.remove)(message.chat.id, disabled.module_key(module))
    if off:
        text = "now switched off for members. Admins can still use them." if changed else "already switched off."
        await reply(message, f"The commands of <b>{module}</b> are {text}")
    else:
        text = "switched on again." if changed else "not switched off as a module."
        await reply(message, f"The commands of <b>{module}</b> are {text}")


@router.message(cmd("disablemodule"))
async def cmd_disablemodule(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_module(message, command, role, off=True)


@router.message(cmd("enablemodule"))
async def cmd_enablemodule(message: Message, command: CommandObject, role: Role | None = None) -> None:
    await _switch_module(message, command, role, off=False)


@router.message(cmd("disabled"))
async def cmd_disabled(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return
    found = await disabled.get(message.chat.id)
    modules = sorted(k.split(":", 1)[1] for k in found if k.startswith("module:"))
    commands = sorted(k for k in found if not k.startswith("module:"))
    if not modules and not commands:
        await reply(message, "Nothing is switched off in this chat.")
        return
    lines = ["Switched off for members (admins can still use them):"]
    if commands:
        lines.append("Commands: " + ", ".join(_slash(c) for c in commands))
    if modules:
        lines.append("Modules: " + ", ".join(f"<code>{m}</code>" for m in modules))
    await reply(message, "\n".join(lines))
