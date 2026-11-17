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
from core.i18n import tr

ABOUT = "disabling.about"

HELP = [
    ("/disable <command>", "disabling.help.switch_a_command_off_for", "common.admins"),
    ("/enable <command>", "disabling.help.switch_a_command_back_on", "common.admins"),
    ("/restrictmodule <module>", "disabling.help.switch_off_every_command_of", "common.admins"),
    ("/unrestrictmodule <module>", "disabling.help.switch_a_module_s_commands", "common.admins"),
    ("/disablemodule <module|all>", "disabling.help.turn_a_module_off_in", "common.chat_owner"),
    ("/enablemodule <module|all>", "disabling.help.turn_a_module_back_on", "common.chat_owner"),
    ("/disabled", "disabling.help.show_what_is_switched_off", "common.everyone"),
]

router = Router(name="disabling")

RIGHT = "can_change_info"


def _slash(name: str) -> str:
    return f"/{name}"


async def _begin(message: Message, role: Role | None, right: str = RIGHT) -> bool:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return False
    return await moderation.begin_chat_command(message, role, right=right) is not None


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
        await reply(message, tr("disabling.use_lt_command_gt_for_example", name=name))
        return
    entry = disabled.lookup(word)
    if entry is None:
        await reply(message, tr("disabling.i_don_t_have_a_command", slash=_slash(esc(word))))
        return
    primary = entry[0]
    if primary in disabled.PROTECTED:
        await reply(message, tr("disabling.can_t_be_switched_off", slash=_slash(primary)))
        return
    chat_id = message.chat.id
    changed = await (disabled.add if off else disabled.remove)(chat_id, primary)
    others = disabled.aliases_of(primary)
    extra = tr("disabling.its_aliases_follow", join=', '.join(_slash(a) for a in others)) if others else ""
    if off:
        if changed:
            await reply(message, tr("disabling.is_now_switched_off_for_members", slash=_slash(primary), extra=extra))
        else:
            await reply(message, tr("disabling.is_already_switched_off", slash=_slash(primary)))
    else:
        if changed:
            await reply(message, tr("disabling.is_switched_on_again", slash=_slash(primary), extra=extra))
        else:
            await reply(message, tr("disabling.isn_t_switched_off", slash=_slash(primary)))


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
        await reply(message, tr("disabling.use_lt_module_gt_modules", name=name, listing=listing))
        return
    if word not in _modules():
        await reply(message, tr("disabling.i_don_t_have_a_module", word=esc(word), listing=listing))
        return
    changed = await (disabled.add if off else disabled.remove)(message.chat.id, disabled.module_key(word))
    if off:
        text = tr("disabling.now_switched_off_for_members_admins") if changed else tr("disabling.already_switched_off_for_members")
    else:
        text = tr("disabling.switched_on_again_for_members") if changed else tr("disabling.not_switched_off_for_members")
    await reply(message, tr("disabling.the_commands_of_are", word=word, text=text))


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
                text=tr("disabling.your_connection_to_has_ended_connections", title=title),
            )
    return tr("disabling.connection_to_this_chat_ended", people=len(people), opt='s' if len(people) != 1 else '') if people else ""


async def _switch_module(message: Message, command: CommandObject, *, off: bool) -> None:
    name = "disablemodule" if off else "enablemodule"
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
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
        await reply(message, tr("disabling.use_lt_module_gt_or_all", name=name, listing=listing))
        return
    chat_id = message.chat.id
    change = disabled.add if off else disabled.remove

    if word == "all":
        changed = [m for m in names if await change(chat_id, disabled.off_key(m))]
        if not off:
            await reply(message, tr("disabling.every_module_is_on_again") if changed else tr("disabling.every_module_was_already_on"))
        elif changed:
            ended = await _end_connections(message) if "connection" in changed else ""
            await reply(message, tr("disabling.modules_are_now_off_in_this", changed=len(changed)) + ended)
        else:
            await reply(message, tr("disabling.everything_is_already_off_here"))
        return

    if word not in names:
        if registry.get(word) is not None:
            await reply(message, tr("disabling.is_part_of_my_core_and", word=esc(word)))
        else:
            await reply(message, tr("disabling.i_don_t_have_a_module", word=esc(word), listing=listing))
        return
    changed = await change(chat_id, disabled.off_key(word))
    if off:
        ended = await _end_connections(message) if changed and word == "connection" else ""
        await reply(message, (tr("disabling.is_now_off_in_this_chat", word=word) + ended) if changed else tr("disabling.is_already_off_here", word=word))
    else:
        await reply(message, tr("disabling.is_on_again_in_this_chat", word=word) if changed else tr("disabling.wasn_t_turned_off_here", word=word))


@router.message(cmd("disablemodule"))
async def cmd_disablemodule(message: Message, command: CommandObject) -> None:
    await _switch_module(message, command, off=True)


@router.message(cmd("enablemodule"))
async def cmd_enablemodule(message: Message, command: CommandObject) -> None:
    await _switch_module(message, command, off=False)


@router.message(cmd("disabled"))
async def cmd_disabled(message: Message) -> None:
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return
    found = await disabled.get(message.chat.id)
    off = sorted(k.split(":", 1)[1] for k in found if k.startswith("off:"))
    members = sorted(k.split(":", 1)[1] for k in found if k.startswith("module:"))
    commands = sorted(k for k in found if ":" not in k)
    if not off and not members and not commands:
        await reply(message, tr("disabling.nothing_is_switched_off_in_this"))
        return
    lines = []
    if commands:
        lines.append(tr("disabling.commands_switched_off_for_members_admins") + ", ".join(_slash(c) for c in commands))
    if members:
        lines.append(tr("disabling.modules_switched_off_for_members_admins") + ", ".join(f"<code>{m}</code>" for m in members))
    if off:
        lines.append(tr("disabling.modules_turned_off_for_everybody") + ", ".join(f"<code>{m}</code>" for m in off))
    await reply(message, "\n".join(lines))
