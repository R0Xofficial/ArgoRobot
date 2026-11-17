"""Captchas for new members"""

import logging
from datetime import timedelta
from typing import Any, Awaitable, Callable

from aiogram import BaseMiddleware, Bot, F, Router
from aiogram.exceptions import TelegramAPIError
from aiogram.filters import CommandObject
from aiogram.types import CallbackQuery, ChatJoinRequest, Message

from core import captchas, info_hooks, moderation
from core.filters import cmd
from core.helpers import reply
from core.moderation import GROUP_TYPES
from core.roles import Role
from core.text import esc, parse_switch
from core.i18n import tr

log = logging.getLogger(__name__)

# Joins are looked at before the other modules (welcome messages wait for the captcha).
PRIORITY = -10

# The name of this module's button in /help.
HELP_TITLE = "captcha.title"

ABOUT = "captcha.about"

HELP_WHO = "common.admins"

HELP = [
    ("/captcha <on|off>", "captcha.help.every_user_who_joins_must"),
    ("/captchamode <button|math|text|text2>", "captcha.help.choose_the_test_a_button"),
    ("/captcharules <on|off>", "captcha.help.newcomers_must_accept_the_chat"),
    ("/captchamutetime <time|off>", "captcha.help.unmute_people_who_haven_t"),
    ("/captchakick <on|off>", "captcha.help.remove_people_who_haven_t"),
    ("/captchakicktime <time>", "captcha.help.how_long_people_get_before"),
    ("/setcaptchatext <text>", "captcha.help.change_the_text_of_the"),
    ("/resetcaptchatext", "captcha.help.back_to_the_default_button"),
]

router = Router(name="captcha")

RIGHT = "can_change_info"  # the admin right needed to change these settings
LOOK_RIGHT = "can_manage_chat"  # every admin holds it: enough to look
MAX_JOINED = 10  # people tested for one "joined" message
MAX_BUTTON_TEXT = 40


# --- Joins ---------------------------------------------------------------------------------------

class JoinMiddleware(BaseMiddleware):
    """
    Looks at every message of the router before its handlers do, without taking the message
    away from the modules after it: newcomers are muted and tested, and the captcha of
    someone who left is cleaned up.
    """

    async def __call__(self, handler: Callable[[Any, dict[str, Any]], Awaitable[Any]], event: Any, data: dict[str, Any]) -> Any:
        try:
            if getattr(event.chat, "type", None) in GROUP_TYPES:
                bot = data.get("bot") or event.bot
                if event.new_chat_members:
                    people = [u for u in event.new_chat_members if not u.is_bot and u.id != bot.id]
                    for index, user in enumerate(people[:MAX_JOINED]):
                        await captchas.hold(bot, event.chat, user, event.message_id, index)
                elif event.left_chat_member is not None:
                    await captchas.drop_user(bot, event.chat.id, event.left_chat_member.id)
        except Exception:
            log.exception("Handling a join for captchas failed")  # never let a captcha bug swallow the message
        return await handler(event, data)


router.message.outer_middleware(JoinMiddleware())


async def _info_flags(bot, user_id, chat_id):
    """For /info: whether the person is muted until they solve a captcha (it turns their Status into Muted (No Captcha))."""
    if chat_id is None:
        return []
    row = await captchas.find(chat_id, user_id)
    return [(tr("captcha.captcha_pending"), row is not None and row["kind"] == "join")]


info_hooks.register("captcha", _info_flags)


@router.chat_join_request()
async def on_join_request(request: ChatJoinRequest, bot: Bot) -> None:
    if request.chat.type not in GROUP_TYPES:
        return
    await captchas.hold_request(bot, request.chat, request.from_user, request.user_chat_id)


# --- Buttons ---------------------------------------------------------------------------------------

def _pending_id(callback: CallbackQuery, parts_wanted: int) -> tuple[int, int] | None:
    parts = (callback.data or "").split(":")
    if len(parts) != parts_wanted or not all(p.isdigit() for p in parts[1:]):
        return None
    return int(parts[1]), int(parts[2]) if parts_wanted == 3 else 0


@router.callback_query(F.data.startswith(f"{captchas.PREFIX}:"))
async def cb_test(callback: CallbackQuery) -> None:
    found = _pending_id(callback, 3)
    if found is None:
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    row = await captchas.get(found[0])
    if row is None:
        await callback.answer(tr("common.you_re_already_verified_or_this"), show_alert=True)
        return
    if callback.from_user.id != row["user_id"]:
        await callback.answer(tr("captcha.this_button_isn_t_for_you"), show_alert=True)
        return
    await callback.answer(await captchas.press(callback.bot, row, found[1], callback.from_user))


@router.callback_query(F.data.startswith(f"{captchas.WELCOME_PREFIX}:"))
async def cb_welcome(callback: CallbackQuery) -> None:
    """The button under a welcome message: the same for everybody, it looks up who pressed it."""
    value = (callback.data or "").split(":", 1)[1]
    if not value.lstrip("-").isdigit():
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    chat_id = int(value)
    here = getattr(getattr(callback.message, "chat", None), "id", chat_id)
    if here != chat_id:
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    note, alert, url = await captchas.welcome_pressed(callback.bot, chat_id, callback.from_user)
    await callback.answer(note, show_alert=alert, url=url)


@router.callback_query(F.data.startswith(f"{captchas.RULES_PREFIX}:"))
async def cb_rules(callback: CallbackQuery) -> None:
    found = _pending_id(callback, 2)
    if found is None:
        await callback.answer(tr("common.this_button_is_not_valid"), show_alert=True)
        return
    row = await captchas.get(found[0])
    if row is None:
        await callback.answer(tr("captcha.this_captcha_has_expired"), show_alert=True)
        return
    if callback.from_user.id != row["user_id"]:
        await callback.answer(tr("captcha.this_isn_t_for_you"), show_alert=True)
        return
    await callback.answer(await captchas.accept_rules(callback.bot, row, callback.from_user))


# --- Settings --------------------------------------------------------------------------------------

async def _begin(message: Message, role: Role | None, *, change: bool) -> bool:
    """The chat's own business: admins only, never the bot's staff."""
    if message.chat.type not in GROUP_TYPES:
        await reply(message, tr("common.this_command_only_works_in_groups"))
        return False
    right = RIGHT if change else LOOK_RIGHT
    return await moderation.begin_chat_command(message, role, right=right) is not None


def _on_off(value: bool) -> str:
    return tr("common.on") if value else tr("common.off")


def _time(seconds: int) -> str:
    return moderation.format_duration(timedelta(seconds=seconds))


def _summary(config: dict[str, Any]) -> str:
    mute = tr("captcha.stay_muted_until_solved") if not config["mute_seconds"] else tr("captcha.unmuted_after", time=_time(config['mute_seconds']))
    button = f"<code>{esc(config['button_text'])}</code>" if config["button_text"] else tr("captcha.default", tr=esc(tr(captchas.DEFAULT_BUTTON)))
    return (
        tr("captcha.captcha_enabled_mode_rules_unsolved_kick", on_off=_on_off(config['enabled']), mode=config['mode'], on_off2=_on_off(config['rules']), mute=mute, on_off3=_on_off(config['kick']), time=_time(config['kick_seconds']), button=button)
    )


async def _rights_warning(message: Message) -> str:
    """Sentences about what keeps captchas from working here, else an empty string."""
    note = ""
    if not await captchas.welcome_available(message.chat.id):
        note = tr("captcha.welcome_messages_are_currently_off_captchas")
    try:
        member = await message.bot.get_chat_member(message.chat.id, message.bot.id)
    except TelegramAPIError:
        return note
    status = str(getattr(member.status, "value", member.status))
    if status == "creator" or (status == "administrator" and getattr(member, "can_restrict_members", False)):
        return note
    return note + tr("captcha.i_can_t_mute_anybody_yet")


@router.message(cmd("captcha"))
async def cmd_captcha(message: Message, command: CommandObject, role: Role | None = None) -> None:
    words = (command.args or "").split()
    argument = words[0] if words else ""
    if not argument:
        if await _begin(message, role, change=False):
            warning = "" if await captchas.welcome_available(message.chat.id) else tr("captcha.welcome_messages_are_off_captchas_only")
            await reply(message, _summary(await captchas.settings(message.chat.id)) + warning + tr("captcha.use_captcha_on_or_captcha_off"))
        return
    enabled = parse_switch(argument)
    if enabled is None:
        await reply(message, tr("captcha.use_captcha_on_or_captcha_off_2"))
        return
    if not await _begin(message, role, change=True):
        return
    config = await captchas.settings(message.chat.id)
    if config["enabled"] == enabled:
        await reply(message, tr("captcha.captchas_are_already", on_off=_on_off(enabled)))
        return
    await captchas.update(message.chat.id, enabled=enabled)
    if enabled:
        await reply(message, tr("captcha.captchas_are_on_new_members_are") + await _rights_warning(message))
        return
    freed = await captchas.release_chat(message.bot, message.chat.id)
    await reply(message, tr("captcha.captchas_are_off") + (tr("captcha.waiting_member_let_in", freed=freed, opt='s' if freed != 1 else '') if freed else ""))


@router.message(cmd("captchamode"))
async def cmd_captchamode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = tr("captcha.use_captchamode_button_math_text_or")
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, tr("captcha.the_captcha_mode_is", mode=config['mode'], usage=usage))
        return
    if argument not in captchas.MODES:
        await reply(message, usage)
        return
    if not await _begin(message, role, change=True):
        return
    if argument == "text2" and not captchas.picture_works():
        await reply(message, tr("captcha.i_can_t_draw_pictures_on"))
        return
    await captchas.update(message.chat.id, mode=argument)
    await reply(message, tr("captcha.the_captcha_mode_is_now", argument=argument))


@router.message(cmd("captcharules"))
async def cmd_captcharules(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip()
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            note = tr("captcha.this_chat_has_no_rules_yet") if config["rules"] and await captchas.rules_missing(message.chat.id) else ""
            await reply(message, tr("captcha.accepting_the_rules_is_use_captcharules", on_off=_on_off(config['rules']), note=note))
        return
    enabled = parse_switch(argument)
    if enabled is None:
        await reply(message, tr("captcha.use_captcharules_on_or_captcharules_off"))
        return
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, rules=enabled)
    text = tr("captcha.newcomers_must_accept_the_rules_after") if enabled else tr("captcha.newcomers_no_longer_have_to_accept")
    if enabled and await captchas.rules_missing(message.chat.id):
        text += tr("captcha.this_chat_has_no_rules_yet_2")
    await reply(message, text)


def _duration(text: str) -> timedelta | None:
    found = moderation.parse_duration(text)
    if found is None or not moderation.MIN_DURATION <= found <= moderation.MAX_DURATION:
        return None
    return found


@router.message(cmd("captchamutetime"))
async def cmd_captchamutetime(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = tr("captcha.use_captchamutetime_30m_or_2h_1d")
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            now = tr("captcha.unsolved_members_stay_muted_until_they") if not config["mute_seconds"] \
                else tr("captcha.unsolved_members_are_unmuted_after", time=_time(config['mute_seconds']))
            await reply(message, f"{now}\n{usage}")
        return
    if argument in ("off", "no"):
        seconds = None
    else:
        found = _duration(argument.split()[0])
        if found is None:
            await reply(message, usage)
            return
        seconds = int(found.total_seconds())
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, mute_seconds=seconds)
    await reply(
        message,
        tr("captcha.unsolved_members_stay_muted_until_they_2") if seconds is None
        else tr("captcha.members_who_haven_t_solved_the", time=_time(seconds)),
    )


@router.message(cmd("captchakick"))
async def cmd_captchakick(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip()
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, tr("captcha.kicking_is_after_use_captchakick_on", on_off=_on_off(config['kick']), time=_time(config['kick_seconds'])))
        return
    enabled = parse_switch(argument)
    if enabled is None:
        await reply(message, tr("captcha.use_captchakick_on_or_captchakick_off"))
        return
    if not await _begin(message, role, change=True):
        return
    config = await captchas.settings(message.chat.id)
    await captchas.update(message.chat.id, kick=enabled)
    if enabled:
        await reply(message, tr("captcha.people_who_don_t_solve_the", time=_time(config['kick_seconds'])))
    else:
        await reply(message, tr("captcha.people_are_no_longer_removed_for"))


@router.message(cmd("captchakicktime"))
async def cmd_captchakicktime(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = tr("captcha.use_captchakicktime_5m_or_30m_2h")
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, tr("captcha.the_kick_time_is", time=_time(config['kick_seconds']), usage=usage))
        return
    found = _duration(argument.split()[0])
    if found is None:
        await reply(message, usage)
        return
    if not await _begin(message, role, change=True):
        return
    seconds = int(found.total_seconds())
    config = await captchas.settings(message.chat.id)
    await captchas.update(message.chat.id, kick_seconds=seconds)
    hint = "" if config["kick"] else tr("captcha.kicking_is_off_turn_it_on")
    await reply(message, tr("captcha.people_get_to_solve_the_captcha", time=_time(seconds), hint=hint))


@router.message(cmd("setcaptchatext"))
async def cmd_setcaptchatext(message: Message, command: CommandObject, role: Role | None = None) -> None:
    text = " ".join((command.args or "").split())
    if not text:
        await reply(message, tr("captcha.use_setcaptchatext_lt_text_gt_for"))
        return
    if len(text) > MAX_BUTTON_TEXT:
        await reply(message, tr("captcha.that_s_too_long_for_a", MAX_BUTTON_TEXT=MAX_BUTTON_TEXT))
        return
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, button_text=text)
    await reply(message, tr("captcha.the_captcha_button_now_says", text=esc(text)))


@router.message(cmd("resetcaptchatext"))
async def cmd_resetcaptchatext(message: Message, role: Role | None = None) -> None:
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, button_text=None)
    await reply(message, tr("captcha.the_captcha_button_is_back_to", DEFAULT_BUTTON=esc(tr(captchas.DEFAULT_BUTTON))))
