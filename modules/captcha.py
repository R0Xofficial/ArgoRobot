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

log = logging.getLogger(__name__)

# Joins are looked at before the other modules (welcome messages wait for the captcha).
PRIORITY = -10

ABOUT = """
Some chats get a lot of users joining just to spam. They may be trolls, or part of a spam network.

To slow them down, turn captchas on. Everybody who joins is muted and has to pass a test to prove they're a real person before they can speak.

Captchas ride on the welcome message, so they only work while welcome messages are on (/welcome). With welcomes off nobody is muted.

Test types (/captchamode): button (press a button), math (pick the answer to a small sum), text (the code is written in the message; press the scrambled characters in the order of the code) and text2 (the same, but the code is a picture).

How it looks: the newcomer's welcome carries a verify button, the same for everybody. In button mode one press unmutes them; the other modes (and button mode with /captcharules) open a private chat with me where they solve the test. Somebody who is already verified only gets a small pop-up saying so.

If the chat uses join requests, the captcha is sent privately and the request is approved once it is solved.

Time: /captchamutetime unmutes people who haven't solved it after a while (by default they stay muted until they do). /captchakick removes people who run out of time (/captchakicktime). When both are set, the kick comes first.

Examples:
• Turn captchas on: /captcha on
• Use the math test: /captchamode math
• Make people accept the rules too: /captcharules on
• Unmute after a day even if unsolved: /captchamutetime 1d
• Stay muted until solved: /captchamutetime off

I need the right to restrict users (and, for join requests, to invite users).
"""

HELP_WHO = "admins"

HELP = [
    ("/captcha <on|off>", "Every user who joins must solve a captcha. With join requests, the captcha is sent privately. Without an argument it shows the settings."),
    ("/captchamode <button|math|text|text2>", "Choose the test: a button, a sum, a code of scrambled characters, or the same code as a picture."),
    ("/captcharules <on|off>", "Newcomers must accept the chat's rules (/rules) after the captcha, before they can speak."),
    ("/captchamutetime <time|off>", "Unmute people who haven't solved the captcha after this time, like 30m, 2h, 1d or 1w. off: they stay muted until they solve it."),
    ("/captchakick <on|off>", "Remove people who haven't solved the captcha in time. Join requests are dismissed."),
    ("/captchakicktime <time>", "How long people get before they are removed, like 5m. Default 5 minutes."),
    ("/setcaptchatext <text>", "Change the text of the captcha button (button mode)."),
    ("/resetcaptchatext", "Back to the default button text."),
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
    return [("Captcha Pending", row is not None and row["kind"] == "join")]


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
        await callback.answer("This button is not valid.", show_alert=True)
        return
    row = await captchas.get(found[0])
    if row is None:
        await callback.answer("You're already verified, or this captcha has expired.", show_alert=True)
        return
    if callback.from_user.id != row["user_id"]:
        await callback.answer("This button isn't for you.", show_alert=True)
        return
    await callback.answer(await captchas.press(callback.bot, row, found[1], callback.from_user))


@router.callback_query(F.data.startswith(f"{captchas.WELCOME_PREFIX}:"))
async def cb_welcome(callback: CallbackQuery) -> None:
    """The button under a welcome message: the same for everybody, it looks up who pressed it."""
    value = (callback.data or "").split(":", 1)[1]
    if not value.lstrip("-").isdigit():
        await callback.answer("This button is not valid.", show_alert=True)
        return
    chat_id = int(value)
    here = getattr(getattr(callback.message, "chat", None), "id", chat_id)
    if here != chat_id:
        await callback.answer("This button is not valid.", show_alert=True)
        return
    note, alert, url = await captchas.welcome_pressed(callback.bot, chat_id, callback.from_user)
    await callback.answer(note, show_alert=alert, url=url)


@router.callback_query(F.data.startswith(f"{captchas.RULES_PREFIX}:"))
async def cb_rules(callback: CallbackQuery) -> None:
    found = _pending_id(callback, 2)
    if found is None:
        await callback.answer("This button is not valid.", show_alert=True)
        return
    row = await captchas.get(found[0])
    if row is None:
        await callback.answer("This captcha has expired.", show_alert=True)
        return
    if callback.from_user.id != row["user_id"]:
        await callback.answer("This isn't for you.", show_alert=True)
        return
    await callback.answer(await captchas.accept_rules(callback.bot, row, callback.from_user))


# --- Settings --------------------------------------------------------------------------------------

async def _begin(message: Message, role: Role | None, *, change: bool) -> bool:
    """The chat's own business: admins only, never the bot's staff."""
    if message.chat.type not in GROUP_TYPES:
        await reply(message, "This command only works in groups.")
        return False
    right = RIGHT if change else LOOK_RIGHT
    return await moderation.begin_chat_command(message, role, allow_staff=False, right=right) is not None


def _on_off(value: bool) -> str:
    return "on" if value else "off"


def _time(seconds: int) -> str:
    return moderation.format_duration(timedelta(seconds=seconds))


def _summary(config: dict[str, Any]) -> str:
    mute = "stay muted until solved" if not config["mute_seconds"] else f"unmuted after {_time(config['mute_seconds'])}"
    button = esc(config["button_text"]) if config["button_text"] else f"{captchas.DEFAULT_BUTTON} (default)"
    return (
        "<b>Captcha</b>\n"
        f"• <b>Enabled:</b> <code>{_on_off(config['enabled'])}</code>\n"
        f"• <b>Mode:</b> <code>{config['mode']}</code>\n"
        f"• <b>Rules:</b> <code>{_on_off(config['rules'])}</code>\n"
        f"• <b>Unsolved:</b> <code>{mute}</code>\n"
        f"• <b>Kick:</b> <code>{_on_off(config['kick'])}</code> (after <code>{_time(config['kick_seconds'])}</code>)\n"
        f"• <b>Button text:</b> {button}"
    )


async def _rights_warning(message: Message) -> str:
    """Sentences about what keeps captchas from working here, else an empty string."""
    note = ""
    if not await captchas.welcome_available(message.chat.id):
        note = "\n\nWelcome messages are currently off. Captchas only work with welcomes on: turn them on with /welcome on."
    try:
        member = await message.bot.get_chat_member(message.chat.id, message.bot.id)
    except TelegramAPIError:
        return note
    status = str(getattr(member.status, "value", member.status))
    if status == "creator" or (status == "administrator" and getattr(member, "can_restrict_members", False)):
        return note
    return note + "\n\nI can't mute anybody yet: make me an admin with the right to restrict users, or captchas won't work."


@router.message(cmd("captcha"))
async def cmd_captcha(message: Message, command: CommandObject, role: Role | None = None) -> None:
    words = (command.args or "").split()
    argument = words[0] if words else ""
    if not argument:
        if await _begin(message, role, change=False):
            warning = "" if await captchas.welcome_available(message.chat.id) else "\n\nWelcome messages are off: captchas only work with welcomes on."
            await reply(message, _summary(await captchas.settings(message.chat.id)) + warning + "\n\nUse /captcha on or /captcha off.")
        return
    enabled = parse_switch(argument)
    if enabled is None:
        await reply(message, "Use /captcha on or /captcha off.")
        return
    if not await _begin(message, role, change=True):
        return
    config = await captchas.settings(message.chat.id)
    if config["enabled"] == enabled:
        await reply(message, f"Captchas are already <b>{_on_off(enabled)}</b>.")
        return
    await captchas.update(message.chat.id, enabled=enabled)
    if enabled:
        await reply(message, "Captchas are <b>on</b>. New members are muted until they pass the test." + await _rights_warning(message))
        return
    freed = await captchas.release_chat(message.bot, message.chat.id)
    await reply(message, "Captchas are <b>off</b>." + (f" {freed} waiting member{'s' if freed != 1 else ''} let in." if freed else ""))


@router.message(cmd("captchamode"))
async def cmd_captchamode(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = "Use /captchamode button, math, text or text2."
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, f"The captcha mode is <b>{config['mode']}</b>.\n{usage}")
        return
    if argument not in captchas.MODES:
        await reply(message, usage)
        return
    if not await _begin(message, role, change=True):
        return
    if argument == "text2" and not captchas.picture_works():
        await reply(message, "I can't draw pictures on this server, so text2 isn't available. Try text.")
        return
    await captchas.update(message.chat.id, mode=argument)
    await reply(message, f"The captcha mode is now <b>{argument}</b>.")


@router.message(cmd("captcharules"))
async def cmd_captcharules(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip()
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, f"Accepting the rules is <b>{_on_off(config['rules'])}</b>.\nUse /captcharules on or /captcharules off.")
        return
    enabled = parse_switch(argument)
    if enabled is None:
        await reply(message, "Use /captcharules on or /captcharules off.")
        return
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, rules=enabled)
    await reply(
        message,
        "Newcomers must <b>accept the rules</b> after the captcha." if enabled else "Newcomers no longer have to accept the rules.",
    )


def _duration(text: str) -> timedelta | None:
    found = moderation.parse_duration(text)
    if found is None or not moderation.MIN_DURATION <= found <= moderation.MAX_DURATION:
        return None
    return found


@router.message(cmd("captchamutetime"))
async def cmd_captchamutetime(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = "Use /captchamutetime 30m (or 2h, 1d, 1w), or /captchamutetime off."
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            now = "Unsolved members stay muted until they solve it." if not config["mute_seconds"] \
                else f"Unsolved members are unmuted after <b>{_time(config['mute_seconds'])}</b>."
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
        "Unsolved members stay <b>muted until they solve it</b>." if seconds is None
        else f"Members who haven't solved the captcha are unmuted after <b>{_time(seconds)}</b>.",
    )


@router.message(cmd("captchakick"))
async def cmd_captchakick(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip()
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, f"Kicking is <b>{_on_off(config['kick'])}</b> (after {_time(config['kick_seconds'])}).\nUse /captchakick on or /captchakick off.")
        return
    enabled = parse_switch(argument)
    if enabled is None:
        await reply(message, "Use /captchakick on or /captchakick off.")
        return
    if not await _begin(message, role, change=True):
        return
    config = await captchas.settings(message.chat.id)
    await captchas.update(message.chat.id, kick=enabled)
    if enabled:
        await reply(message, f"People who don't solve the captcha within <b>{_time(config['kick_seconds'])}</b> are removed. Join requests are dismissed. Change the time with /captchakicktime.")
    else:
        await reply(message, "People are no longer removed for not solving the captcha.")


@router.message(cmd("captchakicktime"))
async def cmd_captchakicktime(message: Message, command: CommandObject, role: Role | None = None) -> None:
    argument = (command.args or "").strip().lower()
    usage = "Use /captchakicktime 5m (or 30m, 2h, 1d)."
    if not argument:
        if await _begin(message, role, change=False):
            config = await captchas.settings(message.chat.id)
            await reply(message, f"The kick time is <b>{_time(config['kick_seconds'])}</b>.\n{usage}")
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
    hint = "" if config["kick"] else " Kicking is off: turn it on with /captchakick on."
    await reply(message, f"People get <b>{_time(seconds)}</b> to solve the captcha.{hint}")


@router.message(cmd("setcaptchatext"))
async def cmd_setcaptchatext(message: Message, command: CommandObject, role: Role | None = None) -> None:
    text = " ".join((command.args or "").split())
    if not text:
        await reply(message, "Use /setcaptchatext &lt;text&gt;, for example /setcaptchatext I come in peace.")
        return
    if len(text) > MAX_BUTTON_TEXT:
        await reply(message, f"That's too long for a button. Keep it under {MAX_BUTTON_TEXT} characters.")
        return
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, button_text=text)
    await reply(message, f"The captcha button now says <b>{esc(text)}</b>.")


@router.message(cmd("resetcaptchatext"))
async def cmd_resetcaptchatext(message: Message, role: Role | None = None) -> None:
    if not await _begin(message, role, change=True):
        return
    await captchas.update(message.chat.id, button_text=None)
    await reply(message, f"The captcha button is back to <b>{esc(captchas.DEFAULT_BUTTON)}</b>.")
